// Package vision recognizes photos of documents (invoices, contracts, receipts, IDs, tax forms…)
// with a multimodal LLM and returns the transcription plus structured key fields.
// Images are processed in memory only: they are never stored or logged.
package vision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"finadvisor/internal/infrastructure/llm"
)

const (
	MaxImageBytes = 10 << 20
	dailyLimit    = 50 // recognitions per user per day
)

var (
	ErrConsentRequired = errors.New("consent to personal data processing is required")
	ErrRateLimited     = errors.New("daily recognition limit reached")
	ErrUnsupported     = errors.New("unsupported image: use JPEG, PNG or WebP")
	ErrTooLarge        = fmt.Errorf("image is larger than %d MB", MaxImageBytes>>20)
	ErrUnreadable      = errors.New("the model could not read the image, please retry with a clearer photo")
)

var allowedTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true}

type ConsentChecker interface {
	HasConsent(ctx context.Context, userID string) (bool, error)
}

type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, int, error)
}

type Field struct {
	Key   string `json:"key"`   // machine key, e.g. total_amount, date, stir
	Label string `json:"label"` // human label in the requested language
	Value string `json:"value"` // as written in the document
}

type Result struct {
	IsDocument   bool     `json:"is_document"`
	DocumentType string   `json:"document_type"`
	Title        string   `json:"title"`
	Language     string   `json:"language"` // language of the document itself
	Text         string   `json:"text"`     // full transcription, original language
	Fields       []Field  `json:"fields"`
	Summary      string   `json:"summary"`
	Warnings     []string `json:"warnings"`
	Model        string   `json:"model"`
}

type Service struct {
	llm     llm.Client
	consent ConsentChecker
	limiter RateLimiter
}

func NewService(c llm.Client, consent ConsentChecker, l RateLimiter) *Service {
	return &Service{llm: c, consent: consent, limiter: l}
}

// DetectType sniffs the real content type (the client-provided header is not trusted).
func DetectType(img []byte) (string, error) {
	if len(img) == 0 {
		return "", ErrUnsupported
	}
	if len(img) > MaxImageBytes {
		return "", ErrTooLarge
	}
	mime := http.DetectContentType(img)
	if !allowedTypes[mime] {
		return "", ErrUnsupported
	}
	return mime, nil
}

func (s *Service) Recognize(ctx context.Context, userID string, img []byte, lang string) (Result, error) {
	mime, err := DetectType(img)
	if err != nil {
		return Result{}, err
	}
	lang = normalizeLang(lang)
	ok, err := s.consent.HasConsent(ctx, userID)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return Result{}, ErrConsentRequired
	}
	if s.limiter != nil {
		key := "photo:" + userID + ":" + time.Now().UTC().Format("2006-01-02")
		allowed, _, err := s.limiter.Allow(ctx, key, dailyLimit, 24*time.Hour)
		if err != nil {
			return Result{}, err
		}
		if !allowed {
			return Result{}, ErrRateLimited
		}
	}

	temp := 0.0 // transcription must be literal
	req := llm.Request{
		Messages: []llm.Message{
			{Role: "system", Content: systemPrompt(lang)},
			{Role: "user", Parts: []llm.ContentPart{llm.TextPart(userPrompt), llm.ImagePart(mime, img)}},
		},
		JSONMode:    true,
		MaxTokens:   8000,
		Temperature: &temp,
	}
	msg, _, err := s.llm.Chat(ctx, req)
	if err != nil {
		return Result{}, err
	}
	res, ok := parse(msg.Content)
	if !ok {
		return Result{}, ErrUnreadable
	}
	res.Model = s.llm.Model()
	return res, nil
}

func parse(content string) (Result, bool) {
	var r Result
	if err := json.Unmarshal([]byte(llm.ExtractJSON(content)), &r); err != nil {
		return r, false
	}
	if r.DocumentType == "" && r.Text == "" && r.Summary == "" {
		return r, false
	}
	if r.DocumentType == "" {
		r.DocumentType = "other"
	}
	if r.Fields == nil {
		r.Fields = []Field{}
	}
	if r.Warnings == nil {
		r.Warnings = []string{}
	}
	r.Text = strings.TrimSpace(r.Text)
	return r, true
}

func normalizeLang(l string) string {
	switch l {
	case "uz", "ru", "en":
		return l
	}
	return "ru"
}
