package calculator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"time"
)

const (
	KindLoan      = "loan"
	KindTax       = "tax"
	KindBreakeven = "breakeven"
)

// Engine is the contract other modules (advisor) depend on. Implementations must be
// deterministic: same input — same output.
type Engine interface {
	Loan(ctx context.Context, in LoanInput) (LoanResult, error)
	Tax(ctx context.Context, in TaxInput) (TaxResult, error)
	Breakeven(ctx context.Context, in BreakevenInput) (BreakevenResult, error)
	Rules() TaxRules
}

type Cache interface {
	GetJSON(ctx context.Context, key string, dst any) (bool, error)
	SetJSON(ctx context.Context, key string, v any, ttl time.Duration) error
}

type Service struct {
	rules TaxRules
	cache Cache // optional
	ttl   time.Duration
}

func NewService(rules TaxRules, cache Cache) *Service {
	return &Service{rules: rules, cache: cache, ttl: 24 * time.Hour}
}

func (s *Service) Rules() TaxRules { return s.rules }

func (s *Service) Loan(ctx context.Context, in LoanInput) (LoanResult, error) {
	return cached(ctx, s, KindLoan, in, func() (LoanResult, error) { return Loan(in) })
}

func (s *Service) Tax(ctx context.Context, in TaxInput) (TaxResult, error) {
	return cached(ctx, s, KindTax+":"+s.rules.Version, in, func() (TaxResult, error) { return Tax(s.rules, in) })
}

func (s *Service) Breakeven(ctx context.Context, in BreakevenInput) (BreakevenResult, error) {
	return cached(ctx, s, KindBreakeven, in, func() (BreakevenResult, error) { return Breakeven(in) })
}

// cached memoizes results in Redis keyed by sha256 of the input. Cache failures are
// logged and never fail the calculation.
func cached[I, R any](ctx context.Context, s *Service, kind string, in I, calc func() (R, error)) (R, error) {
	if s.cache == nil {
		return calc()
	}
	b, _ := json.Marshal(in)
	sum := sha256.Sum256(b)
	key := "calc:" + kind + ":" + hex.EncodeToString(sum[:])
	var out R
	if ok, err := s.cache.GetJSON(ctx, key, &out); err == nil && ok {
		return out, nil
	} else if err != nil {
		slog.WarnContext(ctx, "calc cache get", "err", err)
	}
	out, err := calc()
	if err != nil {
		return out, err
	}
	if err := s.cache.SetJSON(ctx, key, out, s.ttl); err != nil {
		slog.WarnContext(ctx, "calc cache set", "err", err)
	}
	return out, nil
}
