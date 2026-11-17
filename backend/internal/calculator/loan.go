// Package calculator contains deterministic financial calculators. These pure functions are
// the single source of truth for numbers: both the REST API and the LLM agent (via tool
// calling) use them, so the AI never invents figures.
package calculator

import (
	"errors"
	"fmt"
	"math"
)

var ErrInvalidInput = errors.New("invalid input")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidInput, fmt.Sprintf(format, a...))
}

// round2 rounds to tiyin (1/100 UZS).
func round2(v float64) float64 { return math.Round(v*100) / 100 }

const (
	MethodAnnuity        = "annuity"
	MethodDifferentiated = "differentiated"
)

type LoanInput struct {
	Amount      float64 `json:"amount"`      // principal, UZS
	AnnualRate  float64 `json:"annual_rate"` // percent per year, e.g. 24
	TermMonths  int     `json:"term_months"`
	Method      string  `json:"method"`       // annuity | differentiated
	GraceMonths int     `json:"grace_months"` // interest-only months at the start (optional)
}

type LoanPayment struct {
	Month     int     `json:"month"`
	Payment   float64 `json:"payment"`
	Principal float64 `json:"principal"`
	Interest  float64 `json:"interest"`
	Balance   float64 `json:"balance"`
}

type LoanResult struct {
	Input          LoanInput     `json:"input"`
	MonthlyPayment float64       `json:"monthly_payment"` // annuity: regular payment; differentiated: first payment
	FirstPayment   float64       `json:"first_payment"`
	LastPayment    float64       `json:"last_payment"`
	TotalPaid      float64       `json:"total_paid"`
	TotalInterest  float64       `json:"total_interest"`
	OverpaymentPct float64       `json:"overpayment_pct"`
	Schedule       []LoanPayment `json:"schedule"`
}

func (in LoanInput) Validate() error {
	switch {
	case in.Amount <= 0 || in.Amount > 1e15:
		return invalid("amount must be > 0")
	case in.AnnualRate < 0 || in.AnnualRate > 200:
		return invalid("annual_rate must be between 0 and 200")
	case in.TermMonths < 1 || in.TermMonths > 600:
		return invalid("term_months must be between 1 and 600")
	case in.GraceMonths < 0 || in.GraceMonths >= in.TermMonths:
		return invalid("grace_months must be >= 0 and < term_months")
	case in.Method != MethodAnnuity && in.Method != MethodDifferentiated:
		return invalid("method must be %q or %q", MethodAnnuity, MethodDifferentiated)
	}
	return nil
}

// Loan builds a full repayment schedule. Every row is rounded to tiyin and the final
// payment absorbs rounding drift so the balance ends exactly at zero.
func Loan(in LoanInput) (LoanResult, error) {
	if in.Method == "" {
		in.Method = MethodAnnuity
	}
	if err := in.Validate(); err != nil {
		return LoanResult{}, err
	}
	r := in.AnnualRate / 100 / 12
	balance := round2(in.Amount)
	n := in.TermMonths - in.GraceMonths // amortizing months
	sched := make([]LoanPayment, 0, in.TermMonths)

	for m := 1; m <= in.GraceMonths; m++ {
		interest := round2(balance * r)
		sched = append(sched, LoanPayment{m, interest, 0, interest, balance})
	}

	annuity := 0.0
	if in.Method == MethodAnnuity {
		if r == 0 {
			annuity = round2(balance / float64(n))
		} else {
			annuity = round2(balance * r / (1 - math.Pow(1+r, -float64(n))))
		}
	}
	basePrincipal := round2(balance / float64(n))

	for i := 1; i <= n; i++ {
		interest := round2(balance * r)
		var principal float64
		if in.Method == MethodAnnuity {
			principal = round2(annuity - interest)
		} else {
			principal = basePrincipal
		}
		if i == n || principal > balance {
			principal = balance
		}
		balance = round2(balance - principal)
		sched = append(sched, LoanPayment{in.GraceMonths + i, round2(principal + interest), principal, interest, balance})
	}

	res := LoanResult{Input: in, Schedule: sched}
	for _, p := range sched {
		res.TotalPaid += p.Payment
		res.TotalInterest += p.Interest
	}
	res.TotalPaid, res.TotalInterest = round2(res.TotalPaid), round2(res.TotalInterest)
	res.OverpaymentPct = round2(res.TotalInterest / in.Amount * 100)
	first := sched[in.GraceMonths]
	res.FirstPayment, res.LastPayment = first.Payment, sched[len(sched)-1].Payment
	res.MonthlyPayment = first.Payment
	return res, nil
}
