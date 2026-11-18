package speech

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"finadvisor/internal/infrastructure/httpx"
)

// fakeVoiceLab imitates api.voicelab.uz.
type fakeVoiceLab struct {
	ttsMode, sttMode string // "ok", "async", "429", "500", "slow", "401"
	polls            atomic.Int32
	gotAuth          string
	gotLang          string
	srv              *httptest.Server
}

func newFake(t *testing.T, tts, stt string) *fakeVoiceLab {
	f := &fakeVoiceLab{ttsMode: tts, sttMode: stt}
	mux := http.NewServeMux()
	errJSON := func(w http.ResponseWriter, code int, c string) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": c}, "request_id": "req_test"})
	}
	mode := func(w http.ResponseWriter, m string) bool {
		switch m {
		case "429":
			errJSON(w, 429, "rate_limited")
		case "500":
			errJSON(w, 500, "internal")
		case "401":
			errJSON(w, 401, "unauthenticated")
		case "slow":
			time.Sleep(300 * time.Millisecond)
			errJSON(w, 500, "late")
		default:
			return false
		}
		return true
	}
	mux.HandleFunc("GET /v1/voices", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "voice_" + r.URL.Query().Get("language")}}})
	})
	mux.HandleFunc("POST /v1/tts", func(w http.ResponseWriter, r *http.Request) {
		f.gotAuth = r.Header.Get("Authorization")
		if r.Header.Get("Idempotency-Key") == "" {
			errJSON(w, 400, "idempotency_key_required")
			return
		}
		if mode(w, f.ttsMode) {
			return
		}
		if f.ttsMode == "async" {
			w.WriteHeader(202)
			json.NewEncoder(w).Encode(map[string]string{"id": "gen1", "status_url": "/v1/tts/generations/gen1"})
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		w.Write([]byte("RIFF....WAVEdata"))
	})
	mux.HandleFunc("GET /v1/tts/generations/gen1", func(w http.ResponseWriter, r *http.Request) {
		if f.polls.Add(1) < 2 {
			json.NewEncoder(w).Encode(map[string]string{"status": "processing"})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "completed", "audio_url": f.srv.URL + "/signed/audio.wav"})
	})
	mux.HandleFunc("GET /signed/audio.wav", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("pre-signed audio must be downloaded without credentials")
		}
		w.Write([]byte("RIFF-async-WAVE"))
	})
	mux.HandleFunc("POST /v1/stt", func(w http.ResponseWriter, r *http.Request) {
		if mode(w, f.sttMode) {
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			errJSON(w, 400, "bad_multipart")
			return
		}
		if _, _, err := r.FormFile("audio"); err != nil {
			errJSON(w, 400, "audio_required")
			return
		}
		f.gotLang = r.FormValue("language")
		w.WriteHeader(202)
		json.NewEncoder(w).Encode(map[string]string{"id": "tr1", "status": "queued"})
	})
	mux.HandleFunc("GET /v1/stt/transcriptions/tr1", func(w http.ResponseWriter, r *http.Request) {
		if f.polls.Add(1) < 2 {
			json.NewEncoder(w).Encode(map[string]string{"status": "processing"})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "completed", "transcript": " bugun 1,2 mln sotdim ", "language": "uz"})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeVoiceLab) provider() *VoiceLab {
	v := NewVoiceLab(f.srv.URL, "vlk_test", nil)
	v.poll = 5 * time.Millisecond
	return v
}

type memCache struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (c *memCache) GetJSON(_ context.Context, k string, dst any) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.m[k]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(b, dst)
}
func (c *memCache) SetJSON(_ context.Context, k string, v any, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, _ := json.Marshal(v)
	c.m[k] = b
	return nil
}

func TestTTSSyncAndAsync(t *testing.T) {
	ctx := context.Background()
	f := newFake(t, "ok", "ok")
	audio, ctype, err := f.provider().TTS(ctx, "salom", "uz", 1)
	if err != nil || ctype != "audio/wav" || !bytes.HasPrefix(audio, []byte("RIFF")) || f.gotAuth != "Bearer vlk_test" {
		t.Fatalf("sync tts: %v %q %q auth=%q", err, ctype, audio, f.gotAuth)
	}
	f = newFake(t, "async", "ok")
	audio, _, err = f.provider().TTS(ctx, "salom", "uz", 1)
	if err != nil || string(audio) != "RIFF-async-WAVE" || f.polls.Load() < 2 {
		t.Fatalf("async tts: %v %q polls=%d", err, audio, f.polls.Load())
	}
}

func TestSTTPollsUntilCompleted(t *testing.T) {
	f := newFake(t, "ok", "ok")
	text, lang, err := f.provider().STT(context.Background(), []byte("OggS..."), "audio/ogg", "uz")
	if err != nil || text != "bugun 1,2 mln sotdim" || lang != "uz" || f.gotLang != "uz" {
		t.Fatalf("stt = %q %q %v (lang sent %q)", text, lang, err, f.gotLang)
	}
}

func TestProviderFailuresAreUnavailable(t *testing.T) {
	for _, m := range []string{"401", "429", "500"} {
		f := newFake(t, m, m)
		if _, _, err := f.provider().TTS(context.Background(), "x", "uz", 1); !errors.Is(err, ErrUnavailable) {
			t.Errorf("tts %s: %v", m, err)
		}
		if _, _, err := f.provider().STT(context.Background(), []byte("x"), "audio/ogg", "uz"); !errors.Is(err, ErrUnavailable) {
			t.Errorf("stt %s: %v", m, err)
		}
	}
	f := newFake(t, "slow", "slow")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, err := f.provider().TTS(ctx, "x", "uz", 1); !errors.Is(err, ErrUnavailable) {
		t.Errorf("timeout: %v", err)
	}
	if _, _, err := NewVoiceLab(f.srv.URL, "", nil).TTS(context.Background(), "x", "uz", 1); !errors.Is(err, ErrUnavailable) {
		t.Errorf("no key: %v", err)
	}
}

// countingProvider wraps the fake to count real TTS calls.
type countingProvider struct {
	Provider
	tts atomic.Int32
}

func (c *countingProvider) TTS(ctx context.Context, text, lang string, speed float64) ([]byte, string, error) {
	c.tts.Add(1)
	return c.Provider.TTS(ctx, text, lang, speed)
}

func TestSpeakCachesAndPreparesText(t *testing.T) {
	f := newFake(t, "ok", "ok")
	p := &countingProvider{Provider: f.provider()}
	s, err := NewService(p, &memCache{m: map[string][]byte{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, _, err := s.Speak(context.Background(), "u", "Hozir **4 200 000** so'm bor.", "uz", 1); err != nil {
			t.Fatal(err)
		}
	}
	if p.tts.Load() != 1 {
		t.Errorf("tts called %d times, want 1 (cache)", p.tts.Load())
	}
	cases := map[string]string{
		"Hozir **4 200 000** so'm":        "Hozir 4,2 mln so'm",
		"oyiga ~1 797 617, foyda 6000000": "oyiga 1,8 mln, foyda 6 mln",
		"500 000 so'm qarz":               "500 ming so'm qarz",
		"2 000 000 000 aylanma":           "2 mlrd aylanma",
		"24 oy, 30%":                      "24 oy, 30%",
	}
	for in, want := range cases {
		if got := s.SpeakableText(in, "uz"); got != want {
			t.Errorf("uz %q → %q, want %q", in, got, want)
		}
	}
	if got := s.SpeakableText("Свободно **4 200 000** сум", "ru"); got != "Свободно 4,2 млн сум" {
		t.Errorf("ru → %q", got)
	}
	if _, _, err := s.Speak(context.Background(), "u", "x", "uz", 5); !errors.Is(err, ErrValidation) {
		t.Errorf("speed 5: %v", err)
	}
}

func upload(t *testing.T, h http.Handler, data []byte) *httptest.ResponseRecorder {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("audio", "v.webm")
	fw.Write(data)
	mw.Close()
	req := httptest.NewRequest("POST", STTRoute, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req = req.WithContext(httpx.WithUserID(req.Context(), "u"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHandlers(t *testing.T) {
	webm := append([]byte{0x1A, 0x45, 0xDF, 0xA3}, bytes.Repeat([]byte{0}, 64)...)
	mux := func(p Provider) *http.ServeMux {
		s, _ := NewService(p, nil, nil)
		m := http.NewServeMux()
		NewHandler(s).Register(m, func(h http.Handler) http.Handler { return h })
		return m
	}
	ok := mux(newFake(t, "ok", "ok").provider())
	if rec := upload(t, ok, webm); rec.Code != 200 || !strings.Contains(rec.Body.String(), "sotdim") {
		t.Errorf("stt: %d %s", rec.Code, rec.Body)
	}
	if rec := upload(t, ok, []byte("not audio at all")); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text as audio: %d", rec.Code)
	}
	if rec := upload(t, ok, append(webm, make([]byte, MaxAudioBytes)...)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("3 MB audio: %d", rec.Code)
	}
	down := mux(newFake(t, "500", "500").provider())
	if rec := upload(t, down, webm); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "voice_unavailable") {
		t.Errorf("provider down: %d %s", rec.Code, rec.Body)
	}
	req := httptest.NewRequest("POST", "/voice/tts", strings.NewReader(`{"text":"salom","lang":"uz"}`))
	req = req.WithContext(httpx.WithUserID(req.Context(), "u"))
	rec := httptest.NewRecorder()
	ok.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "audio/wav" {
		t.Errorf("tts: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	_, _ = io.Discard.Write(nil)
}
