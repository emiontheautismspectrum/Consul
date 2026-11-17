package calculator

import (
	"errors"
	"testing"
)

func TestBreakevenReference(t *testing.T) {
	// Coffee point: price 25 000, variable 10 000 → contribution 15 000.
	// Fixed 30M → 2 000 cups to break even. Selling 3 000 → profit 15M/month; 60M invested → 4 months.
	res, err := Breakeven(BreakevenInput{
		InitialInvestment: 60_000_000, FixedCostsMonthly: 30_000_000,
		PricePerUnit: 25_000, VariableCostPerUnit: 10_000, ExpectedUnitsMonth: 3_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.BreakevenUnitsMonth != 2_000 || res.BreakevenRevenue != 50_000_000 {
		t.Errorf("breakeven = %.0f units / %.2f UZS", res.BreakevenUnitsMonth, res.BreakevenRevenue)
	}
	if res.ContributionMarginPc != 60 {
		t.Errorf("margin = %.2f, want 60", res.ContributionMarginPc)
	}
	if *res.MonthlyProfit != 15_000_000 || *res.PaybackMonths != 4 {
		t.Errorf("profit %.2f payback %d", *res.MonthlyProfit, *res.PaybackMonths)
	}
	if *res.ROI12MonthsPct != 200 { // (180M − 60M) / 60M
		t.Errorf("roi = %.2f, want 200", *res.ROI12MonthsPct)
	}
	if len(res.Cashflow) != 24 || res.Cashflow[3].Cumulative != 0 || res.Cashflow[23].Cumulative != 300_000_000 {
		t.Errorf("cashflow wrong: m4=%.2f m24=%.2f", res.Cashflow[3].Cumulative, res.Cashflow[23].Cumulative)
	}
}

func TestBreakevenWithLoanAndTax(t *testing.T) {
	// 4% turnover tax: net price 24 000, contribution 14 000; fixed 30M + loan 5M → 2 500 units.
	res, err := Breakeven(BreakevenInput{
		FixedCostsMonthly: 30_000_000, LoanPaymentMonthly: 5_000_000, RevenueTaxPct: 4,
		PricePerUnit: 25_000, VariableCostPerUnit: 10_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.BreakevenUnitsMonth != 2_500 {
		t.Errorf("units = %.0f, want 2500", res.BreakevenUnitsMonth)
	}
	if res.PaybackMonths != nil || res.Cashflow != nil {
		t.Error("payback must be empty without expected volume")
	}
}

func TestBreakevenLossMaking(t *testing.T) {
	res, err := Breakeven(BreakevenInput{
		InitialInvestment: 10_000_000, FixedCostsMonthly: 30_000_000,
		PricePerUnit: 25_000, VariableCostPerUnit: 10_000, ExpectedUnitsMonth: 1_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.PaybackMonths != nil || len(res.Warnings) == 0 || *res.MonthlyProfit >= 0 {
		t.Errorf("loss-making business must have no payback and a warning: %+v", res)
	}
}

func TestBreakevenValidation(t *testing.T) {
	bad := []BreakevenInput{
		{PricePerUnit: 0},
		{PricePerUnit: 100, VariableCostPerUnit: 100},                  // zero contribution → division by zero
		{PricePerUnit: 100, VariableCostPerUnit: 97, RevenueTaxPct: 4}, // tax eats the margin
		{PricePerUnit: 100, FixedCostsMonthly: -1},
		{PricePerUnit: 100, RevenueTaxPct: 100},
	}
	for _, in := range bad {
		if _, err := Breakeven(in); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("Breakeven(%+v) err = %v, want ErrInvalidInput", in, err)
		}
	}
}
