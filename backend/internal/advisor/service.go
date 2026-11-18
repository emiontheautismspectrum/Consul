// Package advisor generates business plans with an LLM that is forced to take every number
// from deterministic calculators (tool calling), and stores the generation history.
package advisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"finadvisor/internal/calculator"
)

var (
	ErrConsentRequired = errors.New("consent to personal data processing is required")
	ErrRateLimited     = errors.New("daily generation limit reached")
	ErrValidation      = errors.New("validation")
)

type ConsentChecker interface {
	HasConsent(ctx context.Context, userID string) (bool, error)
}

type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, int, error)
}

type LoanRequest struct {
	Amount      float64 `json:"amount"`
	AnnualRate  float64 `json:"annual_rate"`
	TermMonths  int     `json:"term_months"`
	Method      string  `json:"method,omitempty"`
	GraceMonths int     `json:"grace_months,omitempty"`
}

// GenerateInput is what the frontend wizard collects step by step.
type GenerateInput struct {
	Lang                string       `json:"lang"`
	Niche               string       `json:"niche"`
	Description         string       `json:"description,omitempty"`
	Region              string       `json:"region,omitempty"`
	EntityType          string       `json:"entity_type,omitempty"` // llc | ie
	Budget              float64      `json:"budget"`                // total startup budget, UZS
	OwnFunds            float64      `json:"own_funds,omitempty"`
	Loan                *LoanRequest `json:"loan,omitempty"`
	PricePerUnit        float64      `json:"price_per_unit,omitempty"`
	VariableCostPerUnit float64      `json:"variable_cost_per_unit,omitempty"`
	ExpectedUnitsMonth  float64      `json:"expected_units_monthly,omitempty"`
	FixedCostsMonthly   float64      `json:"fixed_costs_monthly,omitempty"`
	Employees           int          `json:"employees,omitempty"`
	AvgSalary           float64      `json:"avg_salary,omitempty"`
}

func (in *GenerateInput) normalize() error {
	in.Niche = strings.TrimSpace(in.Niche)
	switch in.Lang {
	case "uz", "ru", "en":
	default:
		in.Lang = "uz"
	}
	if in.EntityType == "" {
		in.EntityType = calculator.EntityLLC
	}
	var errs []string
	if in.Niche == "" || len([]rune(in.Niche)) > 200 {
		errs = append(errs, "niche is required (max 200 chars)")
	}
	if len([]rune(in.Description)) > 2000 {
		errs = append(errs, "description max 2000 chars")
	}
	if in.Budget <= 0 {
		errs = append(errs, "budget must be > 0")
	}
	if in.EntityType != calculator.EntityLLC && in.EntityType != calculator.EntityIE {
		errs = append(errs, "entity_type must be llc or ie")
	}
	for _, v := range []float64{in.OwnFunds, in.PricePerUnit, in.VariableCostPerUnit, in.ExpectedUnitsMonth, in.FixedCostsMonthly, in.AvgSalary} {
		if v < 0 {
			errs = append(errs, "amounts must be >= 0")
			break
		}
	}
	if in.Employees < 0 || in.Employees > 10000 {
		errs = append(errs, "employees must be 0..10000")
	}
	if in.Loan != nil && (in.Loan.Amount <= 0 || in.Loan.TermMonths <= 0 || in.Loan.AnnualRate < 0) {
		errs = append(errs, "loan requires amount > 0, term_months > 0, annual_rate >= 0")
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: %s", ErrValidation, strings.Join(errs, "; "))
	}
	return nil
}

type Service struct {
	agent      *Agent
	model      string
	repo       Repo
	consent    ConsentChecker
	limiter    RateLimiter
	dailyLimit int
}

func NewService(a *Agent, model string, r Repo, c ConsentChecker, l RateLimiter, dailyLimit int) *Service {
	return &Service{agent: a, model: model, repo: r, consent: c, limiter: l, dailyLimit: dailyLimit}
}

func (s *Service) Generate(ctx context.Context, userID string, in GenerateInput) (Plan, error) {
	if err := in.normalize(); err != nil {
		return Plan{}, err
	}
	ok, err := s.consent.HasConsent(ctx, userID)
	if err != nil {
		return Plan{}, err
	}
	if !ok {
		return Plan{}, ErrConsentRequired
	}
	if s.limiter != nil && s.dailyLimit > 0 {
		key := "gen:" + userID + ":" + time.Now().UTC().Format("2006-01-02")
		allowed, _, err := s.limiter.Allow(ctx, key, s.dailyLimit, 24*time.Hour)
		if err != nil {
			return Plan{}, err
		}
		if !allowed {
			return Plan{}, ErrRateLimited
		}
	}

	// Only anonymized data leaves the backend and is persisted.
	in.Niche = Anonymize(in.Niche)
	in.Description = Anonymize(in.Description)
	in.Region = Anonymize(in.Region)
	inputJSON, _ := json.Marshal(in)

	prompt := "Build a business plan for this entrepreneur. Wizard data (amounts in UZS; 0 or missing means unknown — " +
		"make a labeled assumption):\n" + string(inputJSON)
	res, err := s.agent.Run(ctx, in.Lang, prompt)
	if err != nil {
		return Plan{}, err
	}

	trace, _ := json.Marshal(res.Trace)
	p := Plan{
		Title:     in.Niche,
		Input:     inputJSON,
		Content:   res.Markdown,
		ToolCalls: trace,
		Lang:      in.Lang,
		Model:     s.model,
		TokensIn:  res.Usage.PromptTokens,
		TokensOut: res.Usage.CompletionTokens,
	}
	if res.Structured != nil {
		p.Title = truncateRunes(res.Structured.Title, 255)
		p.Structured, _ = json.Marshal(res.Structured)
	}
	if err := s.repo.Save(ctx, userID, &p); err != nil {
		return Plan{}, err
	}
	return p, nil
}

func (s *Service) List(ctx context.Context, userID string, limit, offset int) ([]Plan, int, error) {
	return s.repo.List(ctx, userID, limit, offset)
}

func (s *Service) Get(ctx context.Context, userID, id string) (Plan, error) {
	return s.repo.Get(ctx, userID, id)
}

func (s *Service) Delete(ctx context.Context, userID, id string) error {
	return s.repo.Delete(ctx, userID, id)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
