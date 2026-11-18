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

// fakeEleven imitates api.elevenlabs.io.
type fakeEleven struct {
	ttsMode, sttMode string // "ok", "429", "500", "401", "slow", "multi"
	gotKey           string
	gotPath          string
	gotBody          map[string]any
	gotLang          string
	gotModel         string
	srv              *httptest.Server
}

func newFake(t *testing.T, tts, stt string) *fakeEleven {
	f := &fakeEleven{ttsMode: tts, sttMode: stt}
	mux := http.NewServeMux()
	errJSON := func(w http.ResponseWriter, code int, status string) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]any{"detail": map[string]string{"status": status, "message": "secret text must not be logged"}})
	}
	mode := func(w http.ResponseWriter, m string) bool {
		switch m {
		case "429":
			errJSON(w, 429, "too_many_concurrent_requests")
		case "500":
			errJSON(w, 500, "internal")
		case "401":
			errJSON(w, 401, "invalid_api_key")
		case "slow":
			time.Sleep(300 * time.Millisecond)
			errJSON(w, 500, "late")
		default:
			return false
		}
		return true
	}
	mux.HandleFunc("POST /v1/text-to-speech/{voice}", func(w http.ResponseWriter, r *http.Request) {
		f.gotKey = r.Header.Get("xi-api-key")
		f.gotPath = r.PathValue("voice") + "?" + r.URL.RawQuery
		_ = json.NewDecoder(r.Body).Decode(&f.gotBody)
		if mode(w, f.ttsMode) {
			return
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write([]byte("ID3-fake-mp3"))
	})
	mux.HandleFunc("POST /v1/speech-to-text", func(w http.ResponseWriter, r *http.Request) {
		f.gotKey = r.Header.Get("xi-api-key")
		if mode(w, f.sttMode) {
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			errJSON(w, 400, "bad_multipart")
			return
		}
		if _, _, err := r.FormFile("file"); err != nil {
			errJSON(w, 400, "file_required")
			return
		}
		f.gotLang, f.gotModel = r.FormValue("language_code"), r.FormValue("model_id")
		if f.sttMode == "multi" {
			json.NewEncoder(w).Encode(map[string]any{"transcripts": []map[string]string{{"text": " bugun 1,2 mln sotdim ", "language_code": "uzb"}}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"language_code": "uzb", "language_probability": 0.98, "text": " bugun 1,2 mln sotdim ", "words": []any{}})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeEleven) provider() *ElevenLabs {
	return NewElevenLabs(f.srv.URL, "xi_test", Options{Voices: map[string]string{"ru": "ruvoice"}})
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

func TestTTSRequestShape(t *testing.T) {
	ctx := context.Background()
	f := newFake(t, "ok", "ok")
	audio, ctype, err := f.provider().TTS(ctx, "salom", "uz", 1.1)
	if err != nil || ctype != "audio/mpeg" || string(audio) != "ID3-fake-mp3" || f.gotKey != "xi_test" {
		t.Fatalf("tts: %v %q %q key=%q", err, ctype, audio, f.gotKey)
	}
	if f.gotPath != DefaultVoice+"?output_format=mp3_44100_128" {
		t.Errorf("path = %q", f.gotPath)
	}
	if f.gotBody["model_id"] != "eleven_v3" || f.gotBody["text"] != "salom" {
		t.Errorf("body = %v", f.gotBody)
	}
	if _, ok := f.gotBody["voice_settings"]; ok {
		t.Error("Eleven v3 has no speed setting; voice_settings must not be sent")
	}
	if _, ok := f.gotBody["language_code"]; ok {
		t.Error("Eleven v3 rejects language_code uz; it must not be sent")
	}
	// A v2 model takes speed, clamped to the 0.7–1.2 range the API accepts.
	v2 := NewElevenLabs(f.srv.URL, "xi_test", Options{TTSModel: "eleven_flash_v2_5", Voices: map[string]string{"ru": "ruvoice"}})
	if _, _, err := v2.TTS(ctx, "привет", "ru", 2); err != nil {
		t.Fatal(err)
	}
	vs, _ := f.gotBody["voice_settings"].(map[string]any)
	if vs["speed"] != 1.2 || !strings.HasPrefix(f.gotPath, "ruvoice?") {
		t.Errorf("v2 body = %v path = %q", f.gotBody, f.gotPath)
	}
}

func TestSTTParsesBothResponseShapes(t *testing.T) {
	for _, mode := range []string{"ok", "multi"} {
		f := newFake(t, "ok", mode)
		text, lang, err := f.provider().STT(context.Background(), []byte("OggS..."), "audio/ogg", "uz")
		if err != nil || text != "bugun 1,2 mln sotdim" || lang != "uz" || f.gotLang != "uz" || f.gotModel != "scribe_v2" {
			t.Fatalf("%s: stt = %q %q %v (lang sent %q, model %q)", mode, text, lang, err, f.gotLang, f.gotModel)
		}
	}
	// Auto-detect: no language_code is sent.
	f := newFake(t, "ok", "ok")
	if _, _, err := f.provider().STT(context.Background(), []byte("x"), "audio/ogg", "auto"); err != nil || f.gotLang != "" {
		t.Errorf("auto: err=%v lang sent %q", err, f.gotLang)
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
	if _, _, err := NewElevenLabs(f.srv.URL, "", Options{}).TTS(context.Background(), "x", "uz", 1); !errors.Is(err, ErrUnavailable) {
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
		"Hozir **4 200 000** so'm":        "Hozir 4 million 200 ming so'm",
		"oyiga ~1 797 617, foyda 6000000": "oyiga 1 million 798 ming, foyda 6 million",
		"500 000 so'm qarz":               "500 ming so'm qarz",
		"2 000 000 000 aylanma":           "2 milliard aylanma",
		"1 250 000 000 va 3 500":          "1 milliard 250 million va 3500",
		"24 oy, 30%":                      "24 oy, 30%",
	}
	for in, want := range cases {
		if got := s.SpeakableText(in, "uz"); got != want {
			t.Errorf("uz %q → %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"Свободно **4 200 000** сум": "Свободно 4 миллиона 200 тысяч сум",
		"1 000 000 и 21 000":         "1 миллион и 21 тысяча",
		"15 000 000 и 112 000":       "15 миллионов и 112 тысяч",
	} {
		if got := s.SpeakableText(in, "ru"); got != want {
			t.Errorf("ru %q → %q, want %q", in, got, want)
		}
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
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "audio/mpeg" {
		t.Errorf("tts: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	_, _ = io.Discard.Write(nil)
}

func TestGeoRestrictionRedirectIsUnavailableNotFollowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://geo-restriction.elevenlabs.io/", http.StatusFound)
	}))
	defer srv.Close()
	_, _, err := NewElevenLabs(srv.URL, "k", Options{}).TTS(context.Background(), "x", "uz", 1)
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "302") {
		t.Errorf("err = %v", err)
	}
}

func TestProxyIsUsed(t *testing.T) {
	var hit atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Add(1)
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write([]byte("ID3-via-proxy"))
	}))
	defer proxy.Close()
	// The target is unreachable on its own; only the proxy can answer.
	e := NewElevenLabs("http://elevenlabs.invalid", "k", Options{ProxyURL: proxy.URL})
	audio, _, err := e.TTS(context.Background(), "x", "uz", 1)
	if err != nil || string(audio) != "ID3-via-proxy" || hit.Load() != 1 {
		t.Fatalf("audio=%q err=%v proxy hits=%d", audio, err, hit.Load())
	}
}
