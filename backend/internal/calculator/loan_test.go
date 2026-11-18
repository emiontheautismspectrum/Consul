package calculator

import (
	"errors"
	"math"
	"testing"
)

func near(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

func TestLoanAnnuityReference(t *testing.T) {
	// 100 mln UZS, 24% per year, 12 months. Reference: P = A·r / (1 − (1+r)^−n), r = 2%.
	res, err := Loan(LoanInput{Amount: 100_000_000, AnnualRate: 24, TermMonths: 12, Method: MethodAnnuity})
	if err != nil {
		t.Fatal(err)
	}
	if res.MonthlyPayment != 9_455_959.66 {
		t.Errorf("monthly payment = %.2f, want 9455959.66", res.MonthlyPayment)
	}
	if !near(res.TotalPaid, 113_471_515.95, 0.05) {
		t.Errorf("total paid = %.2f, want ≈113471515.95", res.TotalPaid)
	}
	assertScheduleConsistent(t, res)
}

func TestLoanDifferentiatedReference(t *testing.T) {
	// Interest for differentiated = r·A·(n+1)/2 = 0.02·100M·6.5 = 13M.
	res, err := Loan(LoanInput{Amount: 100_000_000, AnnualRate: 24, TermMonths: 12, Method: MethodDifferentiated})
	if err != nil {
		t.Fatal(err)
	}
	if !near(res.TotalInterest, 13_000_000, 0.05) {
		t.Errorf("total interest = %.2f, want 13000000", res.TotalInterest)
	}
	if res.FirstPayment != 10_333_333.33 {
		t.Errorf("first payment = %.2f, want 10333333.33", res.FirstPayment)
	}
	if res.LastPayment >= res.FirstPayment {
		t.Errorf("differentiated payments must decrease: first %.2f last %.2f", res.FirstPayment, res.LastPayment)
	}
	assertScheduleConsistent(t, res)
}

func TestLoanDifferentiatedCheaperThanAnnuity(t *testing.T) {
	a, _ := Loan(LoanInput{Amount: 50_000_000, AnnualRate: 22, TermMonths: 36, Method: MethodAnnuity})
	d, _ := Loan(LoanInput{Amount: 50_000_000, AnnualRate: 22, TermMonths: 36, Method: MethodDifferentiated})
	if d.TotalInterest >= a.TotalInterest {
		t.Errorf("differentiated interest %.2f should be < annuity %.2f", d.TotalInterest, a.TotalInterest)
	}
}

func TestLoanEdgeCases(t *testing.T) {
	cases := []struct {
		name string
		in   LoanInput
		want func(t *testing.T, r LoanResult)
	}{
		{"zero rate", LoanInput{Amount: 1_200_000, AnnualRate: 0, TermMonths: 12, Method: MethodAnnuity},
			func(t *testing.T, r LoanResult) {
				if r.MonthlyPayment != 100_000 || r.TotalInterest != 0 {
					t.Errorf("got payment %.2f interest %.2f", r.MonthlyPayment, r.TotalInterest)
				}
			}},
		{"one month", LoanInput{Amount: 1_000_000, AnnualRate: 12, TermMonths: 1, Method: MethodDifferentiated},
			func(t *testing.T, r LoanResult) {
				if r.TotalPaid != 1_010_000 {
					t.Errorf("total = %.2f, want 1010000", r.TotalPaid)
				}
			}},
		{"rounding drift absorbed", LoanInput{Amount: 1_000_000, AnnualRate: 17.5, TermMonths: 7, Method: MethodAnnuity},
			func(t *testing.T, r LoanResult) { assertScheduleConsistent(t, r) }},
		{"grace period", LoanInput{Amount: 12_000_000, AnnualRate: 24, TermMonths: 12, GraceMonths: 3, Method: MethodDifferentiated},
			func(t *testing.T, r LoanResult) {
				if r.Schedule[0].Principal != 0 || r.Schedule[0].Interest != 240_000 {
					t.Errorf("grace month = %+v", r.Schedule[0])
				}
				if r.Schedule[3].Principal != 1_333_333.33 {
					t.Errorf("first amortizing principal = %.2f", r.Schedule[3].Principal)
				}
				assertScheduleConsistent(t, r)
			}},
		{"default method is annuity", LoanInput{Amount: 1_000_000, AnnualRate: 10, TermMonths: 10},
			func(t *testing.T, r LoanResult) {
				if r.Input.Method != MethodAnnuity {
					t.Errorf("method = %q", r.Input.Method)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := Loan(c.in)
			if err != nil {
				t.Fatal(err)
			}
			c.want(t, r)
		})
	}
}

func TestLoanValidation(t *testing.T) {
	bad := []LoanInput{
		{Amount: 0, AnnualRate: 10, TermMonths: 12, Method: MethodAnnuity},
		{Amount: 100, AnnualRate: -1, TermMonths: 12, Method: MethodAnnuity},
		{Amount: 100, AnnualRate: 10, TermMonths: 0, Method: MethodAnnuity},
		{Amount: 100, AnnualRate: 10, TermMonths: 12, Method: "balloon"},
		{Amount: 100, AnnualRate: 10, TermMonths: 12, GraceMonths: 12, Method: MethodAnnuity},
	}
	for _, in := range bad {
		if _, err := Loan(in); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("Loan(%+v) err = %v, want ErrInvalidInput", in, err)
		}
	}
}

// assertScheduleConsistent checks the invariants every schedule must satisfy.
func assertScheduleConsistent(t *testing.T, r LoanResult) {
	t.Helper()
	if len(r.Schedule) != r.Input.TermMonths {
		t.Fatalf("schedule has %d rows, want %d", len(r.Schedule), r.Input.TermMonths)
	}
	var principal, paid float64
	for _, p := range r.Schedule {
		if !near(p.Payment, p.Principal+p.Interest, 0.011) {
			t.Errorf("month %d: payment %.2f != principal %.2f + interest %.2f", p.Month, p.Payment, p.Principal, p.Interest)
		}
		principal += p.Principal
		paid += p.Payment
	}
	if last := r.Schedule[len(r.Schedule)-1]; last.Balance != 0 {
		t.Errorf("final balance = %.2f, want 0", last.Balance)
	}
	if !near(principal, r.Input.Amount, 0.01) {
		t.Errorf("sum of principal = %.2f, want %.2f", principal, r.Input.Amount)
	}
	if !near(paid, r.TotalPaid, 0.01) || !near(r.TotalPaid-r.Input.Amount, r.TotalInterest, 0.02) {
		t.Errorf("totals inconsistent: paid %.2f total %.2f interest %.2f", paid, r.TotalPaid, r.TotalInterest)
	}
}

func TestLoanConsulReference(t *testing.T) {
	// Plan reference: 34 000 000 at 24% for 24 months = 1 797 617 so'm per month.
	res, err := Loan(LoanInput{Amount: 34_000_000, AnnualRate: 24, TermMonths: 24, Method: MethodAnnuity})
	if err != nil {
		t.Fatal(err)
	}
	if math.Round(res.MonthlyPayment) != 1_797_617 {
		t.Errorf("monthly payment = %.2f, want 1 797 617", res.MonthlyPayment)
	}
}
