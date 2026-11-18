// Package speech turns voice into text and answers into voice (VoiceLab). Audio and
// transcripts are never stored or logged; any provider failure becomes ErrUnavailable so
// the app falls back to text input.
package speech

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

var ErrUnavailable = errors.New("voice provider unavailable")

// Provider is implemented by VoiceLab and by fakes in tests.
type Provider interface {
	STT(ctx context.Context, audio []byte, mime, lang string) (text, detected string, err error)
	TTS(ctx context.Context, text, lang string, speed float64) (audio []byte, contentType string, err error)
	Voice(ctx context.Context, lang string) (string, error) // voice id used for lang (cache key part)
}

// VoiceLab talks to https://api.voicelab.uz (docs.voicelab.uz). STT is always asynchronous
// (202 + polling); TTS answers 200 with WAV or 202 with a status URL.
type VoiceLab struct {
	base, key string
	http      *http.Client
	poll      time.Duration
	voiceIDs  map[string]string // configured voice per language; empty → first from /v1/voices

	mu       sync.Mutex
	resolved map[string]string
}

func NewVoiceLab(base, key string, voices map[string]string) *VoiceLab {
	return &VoiceLab{base: strings.TrimRight(base, "/"), key: key, http: &http.Client{Timeout: 15 * time.Second},
		poll: 700 * time.Millisecond, voiceIDs: voices, resolved: map[string]string{}}
}

type apiError struct {
	Error struct {
		Code      string `json:"code"`
		Retryable bool   `json:"retryable"`
	} `json:"error"`
	RequestID string `json:"request_id"`
}

// fail logs the provider's error code and request id (never keys or audio) and maps every
// provider problem to ErrUnavailable.
func fail(ctx context.Context, op string, resp *http.Response, err error) error {
	if err != nil {
		slog.WarnContext(ctx, "voicelab: request failed", "op", op, "err", err)
		return fmt.Errorf("%w: %s: %v", ErrUnavailable, op, err)
	}
	var e apiError
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
	slog.WarnContext(ctx, "voicelab: error response", "op", op, "status", resp.StatusCode, "code", e.Error.Code, "request_id", e.RequestID)
	return fmt.Errorf("%w: %s: http %d %s", ErrUnavailable, op, resp.StatusCode, e.Error.Code)
}

func (v *VoiceLab) do(ctx context.Context, method, url string, body io.Reader, ctype, idem string, auth bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+v.key)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	return v.http.Do(req)
}

// Voice returns the configured voice for lang, or the first voice VoiceLab lists for it.
func (v *VoiceLab) Voice(ctx context.Context, lang string) (string, error) {
	if id := v.voiceIDs[lang]; id != "" {
		return id, nil
	}
	v.mu.Lock()
	id := v.resolved[lang]
	v.mu.Unlock()
	if id != "" {
		return id, nil
	}
	resp, err := v.do(ctx, http.MethodGet, v.base+"/v1/voices?language="+lang, nil, "", "", true)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			defer resp.Body.Close()
		}
		return "", fail(ctx, "voices", resp, err)
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Data) == 0 {
		return "", fmt.Errorf("%w: no voice for %s", ErrUnavailable, lang)
	}
	v.mu.Lock()
	v.resolved[lang] = out.Data[0].ID
	v.mu.Unlock()
	return out.Data[0].ID, nil
}

func (v *VoiceLab) TTS(ctx context.Context, text, lang string, speed float64) ([]byte, string, error) {
	if v.key == "" {
		return nil, "", fmt.Errorf("%w: VOICELAB_API_KEY is not set", ErrUnavailable)
	}
	voice, err := v.Voice(ctx, lang)
	if err != nil {
		return nil, "", err
	}
	body, _ := json.Marshal(map[string]any{"text": text, "language": lang, "voice_id": voice, "speed": speed})
	sum := sha256.Sum256(body)
	resp, err := v.do(ctx, http.MethodPost, v.base+"/v1/tts", bytes.NewReader(body), "application/json", "tts-"+hex.EncodeToString(sum[:16]), true)
	if err != nil {
		return nil, "", fail(ctx, "tts", nil, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		audio, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
		if err != nil {
			return nil, "", fail(ctx, "tts", nil, err)
		}
		return audio, "audio/wav", nil
	case http.StatusAccepted:
		var job struct {
			ID        string `json:"id"`
			StatusURL string `json:"status_url"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
			return nil, "", fail(ctx, "tts", nil, err)
		}
		url := job.StatusURL
		if url == "" {
			url = v.base + "/v1/tts/generations/" + job.ID
		} else if strings.HasPrefix(url, "/") {
			url = v.base + url
		}
		var done struct {
			Status   string `json:"status"`
			AudioURL string `json:"audio_url"`
		}
		if err := v.pollJob(ctx, "tts", url, &done); err != nil {
			return nil, "", err
		}
		// audio_url is pre-signed: downloaded without API credentials.
		dl, err := v.do(ctx, http.MethodGet, done.AudioURL, nil, "", "", false)
		if err != nil || dl.StatusCode != http.StatusOK {
			if dl != nil {
				defer dl.Body.Close()
			}
			return nil, "", fail(ctx, "tts_download", dl, err)
		}
		defer dl.Body.Close()
		audio, err := io.ReadAll(io.LimitReader(dl.Body, 20<<20))
		if err != nil {
			return nil, "", fail(ctx, "tts_download", nil, err)
		}
		return audio, "audio/wav", nil
	}
	return nil, "", fail(ctx, "tts", resp, nil)
}

// pollJob polls a job until completed (decoded into dst) or failed, honouring Retry-After.
func (v *VoiceLab) pollJob(ctx context.Context, op, url string, dst any) error {
	for {
		resp, err := v.do(ctx, http.MethodGet, url, nil, "", "", true)
		if err != nil {
			return fail(ctx, op+"_poll", nil, err)
		}
		if resp.StatusCode != http.StatusOK {
			defer resp.Body.Close()
			return fail(ctx, op+"_poll", resp, nil)
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			return fail(ctx, op+"_poll", nil, err)
		}
		var st struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(raw, &st)
		switch st.Status {
		case "completed":
			return json.Unmarshal(raw, dst)
		case "failed":
			return fmt.Errorf("%w: %s job failed", ErrUnavailable, op)
		}
		wait := v.poll
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if d, err := time.ParseDuration(ra + "s"); err == nil && d < 5*time.Second {
				wait = d
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %s: %v", ErrUnavailable, op, ctx.Err())
		case <-time.After(wait):
		}
	}
}

func (v *VoiceLab) STT(ctx context.Context, audio []byte, mime, lang string) (string, string, error) {
	if v.key == "" {
		return "", "", fmt.Errorf("%w: VOICELAB_API_KEY is not set", ErrUnavailable)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="audio"; filename="voice"`)
	h.Set("Content-Type", mime)
	part, _ := mw.CreatePart(h)
	_, _ = part.Write(audio)
	_ = mw.WriteField("language", lang)
	_ = mw.WriteField("include_speakers", "false")
	_ = mw.Close()

	resp, err := v.do(ctx, http.MethodPost, v.base+"/v1/stt", &buf, mw.FormDataContentType(), uuid.NewString(), true)
	if err != nil {
		return "", "", fail(ctx, "stt", nil, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return "", "", fail(ctx, "stt", resp, nil)
	}
	var job struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil || job.ID == "" {
		return "", "", fmt.Errorf("%w: stt: no job id", ErrUnavailable)
	}
	var done struct {
		Transcript string `json:"transcript"`
		Language   string `json:"language"`
	}
	if err := v.pollJob(ctx, "stt", v.base+"/v1/stt/transcriptions/"+job.ID, &done); err != nil {
		return "", "", err
	}
	return strings.TrimSpace(done.Transcript), done.Language, nil
}
