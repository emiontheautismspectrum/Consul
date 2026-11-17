package calculator

import "math"

type BreakevenInput struct {
	InitialInvestment   float64 `json:"initial_investment"`  // startup costs, UZS
	FixedCostsMonthly   float64 `json:"fixed_costs_monthly"` // rent, salaries, utilities…
	PricePerUnit        float64 `json:"price_per_unit"`
	VariableCostPerUnit float64 `json:"variable_cost_per_unit"`
	ExpectedUnitsMonth  float64 `json:"expected_units_monthly"` // optional, enables payback/ROI
	LoanPaymentMonthly  float64 `json:"loan_payment_monthly"`   // optional, from the loan calculator
	RevenueTaxPct       float64 `json:"revenue_tax_pct"`        // optional, e.g. turnover tax 4
}

type CashflowPoint struct {
	Month      int     `json:"month"`
	Profit     float64 `json:"profit"`
	Cumulative float64 `json:"cumulative"`
}

type BreakevenResult struct {
	Input                BreakevenInput  `json:"input"`
	ContributionPerUnit  float64         `json:"contribution_per_unit"`
	ContributionMarginPc float64         `json:"contribution_margin_pct"`
	BreakevenUnitsMonth  float64         `json:"breakeven_units_monthly"`
	BreakevenRevenue     float64         `json:"breakeven_revenue_monthly"`
	MonthlyProfit        *float64        `json:"monthly_profit,omitempty"`
	MarginOfSafetyPct    *float64        `json:"margin_of_safety_pct,omitempty"`
	PaybackMonths        *int            `json:"payback_months,omitempty"` // nil if never pays back
	ROI12MonthsPct       *float64        `json:"roi_12m_pct,omitempty"`
	Cashflow             []CashflowPoint `json:"cashflow_24m,omitempty"`
	Warnings             []string        `json:"warnings,omitempty"`
}

func Breakeven(in BreakevenInput) (BreakevenResult, error) {
	switch {
	case in.PricePerUnit <= 0:
		return BreakevenResult{}, invalid("price_per_unit must be > 0")
	case in.VariableCostPerUnit < 0, in.FixedCostsMonthly < 0, in.InitialInvestment < 0,
		in.ExpectedUnitsMonth < 0, in.LoanPaymentMonthly < 0:
		return BreakevenResult{}, invalid("costs and volumes must be >= 0")
	case in.RevenueTaxPct < 0 || in.RevenueTaxPct >= 100:
		return BreakevenResult{}, invalid("revenue_tax_pct must be in [0, 100)")
	}
	netPrice := in.PricePerUnit * (1 - in.RevenueTaxPct/100)
	contrib := netPrice - in.VariableCostPerUnit
	if contrib <= 0 {
		return BreakevenResult{}, invalid("price after revenue tax must exceed variable cost per unit — every sale loses money")
	}
	monthlyFixed := in.FixedCostsMonthly + in.LoanPaymentMonthly
	units := math.Ceil(monthlyFixed / contrib)
	res := BreakevenResult{
		Input:                in,
		ContributionPerUnit:  round2(contrib),
		ContributionMarginPc: round2(contrib / in.PricePerUnit * 100),
		BreakevenUnitsMonth:  units,
		BreakevenRevenue:     round2(units * in.PricePerUnit),
	}
	if in.ExpectedUnitsMonth == 0 {
		return res, nil
	}

	profit := round2(in.ExpectedUnitsMonth*contrib - monthlyFixed)
	res.MonthlyProfit = &profit
	mos := round2((in.ExpectedUnitsMonth - units) / in.ExpectedUnitsMonth * 100)
	res.MarginOfSafetyPct = &mos
	if in.InitialInvestment > 0 {
		roi := round2((12*profit - in.InitialInvestment) / in.InitialInvestment * 100)
		res.ROI12MonthsPct = &roi
	}
	if profit > 0 {
		p := int(math.Ceil(in.InitialInvestment / profit))
		res.PaybackMonths = &p
	} else {
		res.Warnings = append(res.Warnings, "expected volume is below break-even: the business does not pay back")
	}
	if profit > 0 && *res.PaybackMonths > 36 {
		res.Warnings = append(res.Warnings, "payback period exceeds 36 months")
	}
	cum := -in.InitialInvestment
	for m := 1; m <= 24; m++ {
		cum = round2(cum + profit)
		res.Cashflow = append(res.Cashflow, CashflowPoint{m, profit, cum})
	}
	return res, nil
}
