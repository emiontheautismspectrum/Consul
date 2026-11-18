// Package speech turns voice into text and answers into voice (ElevenLabs: Scribe for
// speech-to-text, Eleven v3 for text-to-speech, the only model family that speaks Uzbek).
// Audio and transcripts are never stored or logged; any provider failure becomes
// ErrUnavailable so the app falls back to text input.
package speech

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

var ErrUnavailable = errors.New("voice provider unavailable")

// Provider is implemented by ElevenLabs and by fakes in tests.
type Provider interface {
	STT(ctx context.Context, audio []byte, mime, lang string) (text, detected string, err error)
	TTS(ctx context.Context, text, lang string, speed float64) (audio []byte, contentType string, err error)
	Voice(ctx context.Context, lang string) (string, error) // voice id used for lang (cache key part)
}

const (
	DefaultBaseURL  = "https://api.elevenlabs.io"
	DefaultTTSModel = "eleven_v3"
	DefaultSTTModel = "scribe_v2"
	// DefaultVoice is a premade ElevenLabs voice; set ELEVENLABS_VOICE_UZ/RU for a better fit.
	DefaultVoice = "JBFqnCBsd6RMkjVDRZzb"
	outputFormat = "mp3_44100_128"
)

// Options tune the ElevenLabs client; zero values mean the defaults above.
type Options struct {
	TTSModel, STTModel string
	// ProxyURL routes requests through an HTTP(S) or SOCKS5 proxy ("socks5://host:1080").
	// ElevenLabs refuses some countries (our VPS is in one), so egress may need a proxy.
	ProxyURL string
	Voices   map[string]string // voice id per language ("uz", "ru")
}

type ElevenLabs struct {
	base, key string
	o         Options
	http      *http.Client
}

func NewElevenLabs(base, key string, o Options) *ElevenLabs {
	if base == "" {
		base = DefaultBaseURL
	}
	if o.TTSModel == "" {
		o.TTSModel = DefaultTTSModel
	}
	if o.STTModel == "" {
		o.STTModel = DefaultSTTModel
	}
	c := &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if o.ProxyURL != "" {
		if u, err := url.Parse(o.ProxyURL); err == nil && u.Host != "" {
			c.Transport = &http.Transport{Proxy: http.ProxyURL(u)}
		} else {
			slog.Error("elevenlabs: ELEVENLABS_PROXY_URL is not a valid URL; ignoring it")
		}
	}
	return &ElevenLabs{base: strings.TrimRight(base, "/"), key: key, o: o, http: c}
}

// fail logs the provider's status and error code (never keys, text or audio) and maps every
// provider problem to ErrUnavailable.
func fail(ctx context.Context, op string, resp *http.Response, err error) error {
	if err != nil {
		slog.WarnContext(ctx, "elevenlabs: request failed", "op", op, "err", err)
		return fmt.Errorf("%w: %s: %v", ErrUnavailable, op, err)
	}
	// {"detail":{"status":"invalid_api_key","message":"…"}} — or a plain string / validation list.
	var e struct {
		Detail json.RawMessage `json:"detail"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
	code := ""
	var d struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(e.Detail, &d) == nil {
		code = d.Status
	}
	if resp.StatusCode == http.StatusFound && strings.Contains(resp.Header.Get("Location"), "geo-restriction") {
		code = "geo_restricted" // the egress IP is in a country ElevenLabs does not serve: use ELEVENLABS_PROXY_URL
	}
	slog.WarnContext(ctx, "elevenlabs: error response", "op", op, "status", resp.StatusCode, "code", code)
	return fmt.Errorf("%w: %s: http %d %s", ErrUnavailable, op, resp.StatusCode, code)
}

func (e *ElevenLabs) Voice(_ context.Context, lang string) (string, error) {
	if id := e.o.Voices[lang]; id != "" {
		return id, nil
	}
	return DefaultVoice, nil
}

func (e *ElevenLabs) TTS(ctx context.Context, text, lang string, speed float64) ([]byte, string, error) {
	if e.key == "" {
		return nil, "", fmt.Errorf("%w: ELEVENLABS_API_KEY is not set", ErrUnavailable)
	}
	voice, _ := e.Voice(ctx, lang)
	body := map[string]any{"text": text, "model_id": e.o.TTSModel}
	// Eleven v3 detects the language itself and rejects language_code "uz"; it also has no
	// speed setting. The v2/flash models take speed (0.7–1.2) but have no Uzbek at all.
	if e.o.TTSModel != "eleven_v3" {
		body["language_code"] = lang
		if speed != 1 && speed > 0 {
			body["voice_settings"] = map[string]any{"speed": min(max(speed, 0.7), 1.2)}
		}
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.base+"/v1/text-to-speech/"+url.PathEscape(voice)+"?output_format="+outputFormat, bytes.NewReader(b))
	if err != nil {
		return nil, "", fail(ctx, "tts", nil, err)
	}
	req.Header.Set("xi-api-key", e.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/mpeg")
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, "", fail(ctx, "tts", nil, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fail(ctx, "tts", resp, nil)
	}
	audio, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil || len(audio) == 0 {
		return nil, "", fail(ctx, "tts", nil, fmt.Errorf("empty audio: %v", err))
	}
	return audio, "audio/mpeg", nil
}

// langFromScribe maps Scribe's ISO 639-3 codes ("uzb", "rus") to the app's languages.
func langFromScribe(c string) string {
	switch strings.ToLower(c) {
	case "uz", "uzb":
		return "uz"
	case "ru", "rus":
		return "ru"
	}
	return ""
}

func (e *ElevenLabs) STT(ctx context.Context, audio []byte, mime, lang string) (string, string, error) {
	if e.key == "" {
		return "", "", fmt.Errorf("%w: ELEVENLABS_API_KEY is not set", ErrUnavailable)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("model_id", e.o.STTModel)
	_ = mw.WriteField("tag_audio_events", "false")
	if lang == "uz" || lang == "ru" { // otherwise Scribe detects the language itself
		_ = mw.WriteField("language_code", lang)
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="audio"`)
	if mime != "" {
		h.Set("Content-Type", mime)
	}
	fw, err := mw.CreatePart(h)
	if err != nil {
		return "", "", fail(ctx, "stt", nil, err)
	}
	_, _ = fw.Write(audio)
	_ = mw.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.base+"/v1/speech-to-text", &buf)
	if err != nil {
		return "", "", fail(ctx, "stt", nil, err)
	}
	req.Header.Set("xi-api-key", e.key)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := e.http.Do(req)
	if err != nil {
		return "", "", fail(ctx, "stt", nil, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fail(ctx, "stt", resp, nil)
	}
	// One channel answers {language_code, text, …}; multichannel answers {transcripts:[…]}.
	var out struct {
		Text         string `json:"text"`
		LanguageCode string `json:"language_code"`
		Transcripts  []struct {
			Text         string `json:"text"`
			LanguageCode string `json:"language_code"`
		} `json:"transcripts"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return "", "", fail(ctx, "stt", nil, err)
	}
	text, code := out.Text, out.LanguageCode
	if text == "" && len(out.Transcripts) > 0 {
		text, code = out.Transcripts[0].Text, out.Transcripts[0].LanguageCode
	}
	return strings.TrimSpace(text), langFromScribe(code), nil
}
