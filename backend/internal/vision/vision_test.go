package vision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"finadvisor/internal/infrastructure/httpx"
	"finadvisor/internal/infrastructure/llm"
)

type fakeLLM struct {
	reply string
	err   error
	got   llm.Request
}

func (f *fakeLLM) Model() string { return "kimi-k3" }
func (f *fakeLLM) Chat(_ context.Context, req llm.Request) (llm.Message, llm.Usage, error) {
	f.got = req
	return llm.Message{Role: "assistant", Content: f.reply}, llm.Usage{}, f.err
}

type consent bool

func (c consent) HasConsent(context.Context, string) (bool, error) { return bool(c), nil }

type limiter struct{ n int }

func (l *limiter) Allow(_ context.Context, _ string, limit int, _ time.Duration) (bool, int, error) {
	l.n++
	return l.n <= limit, limit - l.n, nil
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.Black)
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

const invoiceReply = "```json\n" + `{"is_document":true,"document_type":"invoice","title":"Счёт-фактура № 147",
 "language":"ru","text":"СЧЁТ-ФАКТУРА № 147\nИтого: 14 000 000 сум",
 "fields":[{"key":"total_amount","label":"Итого","value":"14 000 000 сум"}],
 "summary":"Счёт на оплату кофемашины.","warnings":[]}` + "\n```"

func TestRecognizeSendsImageAndParsesFencedJSON(t *testing.T) {
	f := &fakeLLM{reply: invoiceReply}
	res, err := NewService(f, consent(true), nil).Recognize(context.Background(), "u", pngBytes(t), "uz")
	if err != nil {
		t.Fatal(err)
	}
	if res.DocumentType != "invoice" || res.Fields[0].Value != "14 000 000 сум" || res.Model != "kimi-k3" {
		t.Errorf("result = %+v", res)
	}
	user := f.got.Messages[1]
	if len(user.Parts) != 2 || user.Parts[1].Type != "image_url" ||
		!strings.HasPrefix(user.Parts[1].ImageURL.URL, "data:image/png;base64,") {
		t.Errorf("image not sent as data URL: %+v", user.Parts)
	}
	if f.got.Temperature == nil || *f.got.Temperature != 0 || !f.got.JSONMode {
		t.Error("transcription must use temperature 0 and JSON mode")
	}
	if !strings.Contains(f.got.Messages[0].Content, "Uzbek") {
		t.Error("system prompt must use the requested language")
	}
}

func TestRecognizeGuards(t *testing.T) {
	ctx := context.Background()
	img := pngBytes(t)
	if _, err := NewService(&fakeLLM{reply: invoiceReply}, consent(false), nil).Recognize(ctx, "u", img, ""); !errors.Is(err, ErrConsentRequired) {
		t.Errorf("no consent: %v", err)
	}
	if _, err := NewService(&fakeLLM{}, consent(true), nil).Recognize(ctx, "u", []byte("%PDF-1.7 not an image"), ""); !errors.Is(err, ErrUnsupported) {
		t.Errorf("pdf: %v", err)
	}
	if _, err := NewService(&fakeLLM{}, consent(true), nil).Recognize(ctx, "u", make([]byte, MaxImageBytes+1), ""); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too large: %v", err)
	}
	if _, err := NewService(&fakeLLM{reply: "I cannot see anything"}, consent(true), nil).Recognize(ctx, "u", img, ""); !errors.Is(err, ErrUnreadable) {
		t.Errorf("garbage reply: %v", err)
	}
	l := &limiter{n: dailyLimit}
	if _, err := NewService(&fakeLLM{reply: invoiceReply}, consent(true), l).Recognize(ctx, "u", img, ""); !errors.Is(err, ErrRateLimited) {
		t.Errorf("limit: %v", err)
	}
	up := &fakeLLM{err: llm.ErrUpstream}
	if _, err := NewService(up, consent(true), nil).Recognize(ctx, "u", img, ""); !errors.Is(err, llm.ErrUpstream) {
		t.Errorf("upstream: %v", err)
	}
}

func TestNotADocument(t *testing.T) {
	f := &fakeLLM{reply: `{"is_document":false,"document_type":"other","summary":"Фото кота"}`}
	res, err := NewService(f, consent(true), nil).Recognize(context.Background(), "u", pngBytes(t), "ru")
	if err != nil || res.IsDocument || res.Fields == nil || res.Warnings == nil {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func upload(t *testing.T, h http.Handler, field string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if field != "" {
		fw, _ := mw.CreateFormFile(field, "doc.png")
		fw.Write(data)
	}
	mw.WriteField("lang", "en")
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, Route, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req = req.WithContext(httpx.WithUserID(req.Context(), "u"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHandler(t *testing.T) {
	mux := http.NewServeMux()
	NewHandler(NewService(&fakeLLM{reply: invoiceReply}, consent(true), nil)).
		Register(mux, func(h http.Handler) http.Handler { return h })

	rec := upload(t, mux, "file", pngBytes(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out["document_type"] != "invoice" || out["disclaimer"] == "" {
		t.Errorf("body = %v", out)
	}
	if rec := upload(t, mux, "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("missing file: %d", rec.Code)
	}
	if rec := upload(t, mux, "file", []byte("plain text")); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text file: %d", rec.Code)
	}
	if rec := upload(t, mux, "file", make([]byte, MaxImageBytes+128<<10)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("huge file: %d", rec.Code)
	}
}

func TestBuildDrafts(t *testing.T) {
	doc := func(typ string, fields ...Field) *Result {
		return &Result{IsDocument: true, DocumentType: typ, Title: "Счёт-фактура № 147", Fields: fields, Text: "Оплата наличными"}
	}
	r := doc("invoice", Field{Key: "total_amount", Value: "14 000 000 сум"}, Field{Key: "supplier", Value: "ООО Toshkent Savdo"})
	d := BuildDrafts(r)
	if len(d) != 1 || d[0].Kind != "buy" || d[0].Amount != 14_000_000 || d[0].Method != "naqd" || d[0].Counterparty != "ООО Toshkent Savdo" || d[0].Source != "photo" {
		t.Fatalf("draft = %+v", d)
	}
	for in, want := range map[string]int64{"14,000,000.00 UZS": 14_000_000, "1500000": 1_500_000, "3 040 000 so'm": 3_040_000, "Jami: 750 000": 750_000, "abc": 0} {
		if got := parseAmount(in); got != want {
			t.Errorf("parseAmount(%q) = %d, want %d", in, got, want)
		}
	}
	card := doc("receipt", Field{Key: "total", Value: "99 000"})
	card.Text = "Karta orqali to'landi, QR"
	if d := BuildDrafts(card); len(d) != 1 || d[0].Method != "karta" || !d[0].Fiscal {
		t.Errorf("card + QR receipt: %+v", d)
	}
	if d := BuildDrafts(doc("passport", Field{Key: "total", Value: "1000"})); d != nil {
		t.Errorf("a passport must not produce an operation: %+v", d)
	}
	noTotal := doc("invoice", Field{Key: "supplier", Value: "X"})
	if d := BuildDrafts(noTotal); d != nil || len(noTotal.Warnings) == 0 {
		t.Errorf("missing total: %+v warnings %v", d, noTotal.Warnings)
	}
	if d := BuildDrafts(&Result{IsDocument: false, DocumentType: "invoice"}); d != nil {
		t.Error("a non-document must not produce an operation")
	}
}
