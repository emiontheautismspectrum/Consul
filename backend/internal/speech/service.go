package speech

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

//go:embed locales
var locales embed.FS

const (
	MaxAudioBytes = 2 << 20 // ~60 s of browser Opus
	MaxTTSRunes   = 1000
	ttsCacheTTL   = 24 * time.Hour
	sttDailyLimit = 100
	ttsDailyLimit = 300
	providerLimit = 10 * time.Second
)

var (
	ErrRateLimited = errors.New("daily voice limit reached")
	ErrValidation  = errors.New("validation")
)

type Cache interface {
	GetJSON(ctx context.Context, key string, dst any) (bool, error)
	SetJSON(ctx context.Context, key string, v any, ttl time.Duration) error
}

type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, int, error)
}

type Service struct {
	p       Provider
	cache   Cache
	limiter Limiter
	units   map[string]map[string][]string
}

func NewService(p Provider, c Cache, l Limiter) (*Service, error) {
	b, err := locales.ReadFile("locales/units.json")
	if err != nil {
		return nil, err
	}
	s := &Service{p: p, cache: c, limiter: l}
	return s, json.Unmarshal(b, &s.units)
}

func (s *Service) allow(ctx context.Context, kind, userID string, limit int) error {
	if s.limiter == nil {
		return nil
	}
	ok, _, err := s.limiter.Allow(ctx, "voice:"+kind+":"+userID+":"+time.Now().UTC().Format("2006-01-02"), limit, 24*time.Hour)
	if err == nil && !ok {
		return ErrRateLimited
	}
	return nil
}

func normLang(l string) string {
	if l == "ru" {
		return "ru"
	}
	return "uz"
}

func (s *Service) Transcribe(ctx context.Context, userID string, audio []byte, mime, lang string) (string, string, error) {
	if err := s.allow(ctx, "stt", userID, sttDailyLimit); err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(ctx, providerLimit)
	defer cancel()
	if lang != "uz" && lang != "ru" {
		lang = "auto"
	}
	text, detected, err := s.p.STT(ctx, audio, mime, lang)
	if err != nil {
		return "", "", err
	}
	if detected != "ru" && detected != "uz" {
		detected = lang
		if detected == "auto" {
			detected = "uz"
		}
	}
	return text, detected, nil
}

type cachedAudio struct {
	Audio []byte `json:"a"`
	Type  string `json:"t"`
}

// Speak returns audio for text, cached for 24 h by sha256(text+lang+speed+voice).
func (s *Service) Speak(ctx context.Context, userID, text, lang string, speed float64) ([]byte, string, error) {
	lang = normLang(lang)
	if speed == 0 {
		speed = 1
	}
	if speed < 0.5 || speed > 2 {
		return nil, "", fmt.Errorf("%w: speed must be 0.5..2", ErrValidation)
	}
	spoken := s.SpeakableText(text, lang)
	if spoken == "" || len([]rune(spoken)) > MaxTTSRunes {
		return nil, "", fmt.Errorf("%w: text must be 1..%d characters", ErrValidation, MaxTTSRunes)
	}
	ctx, cancel := context.WithTimeout(ctx, providerLimit)
	defer cancel()
	voice, err := s.p.Voice(ctx, lang)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256([]byte(spoken + "\x00" + lang + "\x00" + strconv.FormatFloat(speed, 'f', 2, 64) + "\x00" + voice))
	key := "tts:" + hex.EncodeToString(sum[:])
	if s.cache != nil {
		var c cachedAudio
		if ok, err := s.cache.GetJSON(ctx, key, &c); err == nil && ok {
			return c.Audio, c.Type, nil // cache hits do not count against the daily limit
		}
	}
	if err := s.allow(ctx, "tts", userID, ttsDailyLimit); err != nil {
		return nil, "", err
	}
	audio, ctype, err := s.p.TTS(ctx, spoken, lang, speed)
	if err != nil {
		return nil, "", err
	}
	if s.cache != nil {
		_ = s.cache.SetJSON(ctx, key, cachedAudio{audio, ctype}, ttsCacheTTL)
	}
	return audio, ctype, nil
}

var (
	markdownRe = regexp.MustCompile(`\*\*|__|[*_` + "`" + `#>]`)
	moneyRe    = regexp.MustCompile(`~?(\d{1,3}(?:[ \x{00a0}]\d{3})+|\d{4,})`)
)

// SpeakableText strips markdown and reads amounts the way people say them:
// "**4 200 000**" → "4,2 mln", "500 000" → "500 ming", "1 797 617" → "1,8 mln".
func (s *Service) SpeakableText(text, lang string) string {
	u := s.units[normLang(lang)]
	text = markdownRe.ReplaceAllString(text, "")
	text = moneyRe.ReplaceAllStringFunc(text, func(m string) string {
		n, err := strconv.ParseInt(strings.NewReplacer(" ", "", " ", "", "~", "").Replace(m), 10, 64)
		if err != nil {
			return m
		}
		return spokenAmount(n, u)
	})
	return strings.Join(strings.Fields(text), " ")
}

// form picks the word form for n ("1 миллион", "2 миллиона", "5 миллионов"); one-form
// languages (Uzbek) use the same word after any number.
func form(forms []string, n int64) string {
	if len(forms) < 3 {
		return forms[0]
	}
	switch {
	case n%100 >= 11 && n%100 <= 14, n%10 == 0, n%10 >= 5:
		return forms[2]
	case n%10 == 1:
		return forms[0]
	}
	return forms[1]
}

// spokenAmount says an amount in words the voice reads correctly: 4 200 000 → "4 million
// 200 ming". Digits with commas ("4,2 mln") come out as nonsense ("four million two million").
// Amounts of 10 000 and more are rounded to whole thousands.
func spokenAmount(n int64, u map[string][]string) string {
	if n < 10_000 {
		return strconv.FormatInt(n, 10)
	}
	n = (n + 500) / 1000 * 1000
	var parts []string
	for _, c := range []struct {
		key string
		div int64
	}{{"mlrd", 1_000_000_000}, {"mln", 1_000_000}, {"ming", 1000}} {
		if q := n / c.div; q > 0 {
			parts = append(parts, strconv.FormatInt(q, 10)+" "+form(u[c.key], q))
			n %= c.div
		}
	}
	return strings.Join(parts, " ")
}
