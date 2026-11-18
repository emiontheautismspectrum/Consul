package ledger

import (
	"math"
	"time"
)

// --- bank report ---

type BankMonth struct {
	Month   string `json:"month"` // 2026-10
	Income  int64  `json:"income"`
	Expense int64  `json:"expense"` // purchases + other expenses
	Profit  int64  `json:"profit"`
	Partial bool   `json:"partial"` // the month is not over yet: profit is not final
}

type BankReport struct {
	GeneratedAt    time.Time   `json:"generated_at"`
	Business       Business    `json:"business"`
	Months         []BankMonth `json:"months"`     // closed months, oldest first, then the current one
	AvgProfit      int64       `json:"avg_profit"` // average of the closed months only
	TaxThisMonth   int64       `json:"tax_this_month"`
	OwedToMe       int64       `json:"owed_to_me"`
	OwedToSupplier int64       `json:"owed_to_suppliers"`
	Cash           int64       `json:"cash"`
}

// Bank builds the report a bank wants: income, expenses and profit by month. Profit of the
// current month is marked partial and is NOT part of the average (credit decisions use closed
// months only).
func Bank(st State, now time.Time, months int, p TaxPolicy) BankReport {
	months = min(max(months, 1), 12)
	td := Compute(st, now, p)
	rep := BankReport{GeneratedAt: now.In(Tashkent), Business: st.Business, Cash: td.View.Cash,
		TaxThisMonth: td.View.Tax, OwedToMe: td.View.OwedToMe, Months: []BankMonth{}}
	var sum int64
	n := 0
	for _, m := range Months(st, now, months+1) {
		rep.Months = append(rep.Months, BankMonth{Month: m.Month, Income: m.Income, Expense: m.Buy + m.Exp, Profit: m.Profit, Partial: m.Partial})
		if !m.Partial && (m.Income != 0 || m.Buy != 0 || m.Exp != 0) {
			sum += m.Profit
			n++
		}
	}
	if n > 0 {
		rep.AvgProfit = roundSom(float64(sum) / float64(n))
	}
	for _, u := range td.Upcoming {
		if u.Kind == "supplier" {
			rep.OwedToSupplier += u.Amount
		}
	}
	return rep
}

// --- what if ---

type Payment struct {
	Day    int   `json:"day"`    // 1..30 counted from tomorrow
	Amount int64 `json:"amount"` // positive = money in, negative = money out
}

type WhatIfInput struct {
	Kind string `json:"kind"` // buy | helper | loan
	// buy: Amount paid now, or Payments for instalments.
	Amount   int64     `json:"amount"`
	Payments []Payment `json:"payments"`
	// helper: Amount per month; UpliftPct extra sales (percent) from day 10.
	UpliftPct float64 `json:"uplift_pct"`
	// loan: Amount received on day 1, repaid monthly (annuity) from day 30.
	AnnualRate float64 `json:"annual_rate"`
	TermMonths int     `json:"term_months"`
}

type WhatIfResult struct {
	Baseline    []ForecastPoint `json:"baseline"`
	Scenario    []ForecastPoint `json:"scenario"`
	Lowest      ForecastPoint   `json:"lowest"`
	LowestDay   int             `json:"lowest_day"`
	Verdict     string          `json:"verdict"` // ok | tight (below the reserve) | short (cash runs out)
	Assumptions []string        `json:"assumptions"`
}

// AnnuityPayment is the monthly payment of an annuity loan.
func AnnuityPayment(amount float64, annualRate float64, months int) float64 {
	r := annualRate / 100 / 12
	if months <= 0 {
		return 0
	}
	if r == 0 {
		return amount / float64(months)
	}
	return amount * r / (1 - math.Pow(1+r, -float64(months)))
}

// CogsShare is the cost share of income over the closed months (purchases / income); ok is
// false when there is no history.
func CogsShare(st State, now time.Time) (float64, bool) {
	var inc, buy int64
	for _, m := range Months(st, now, 4) {
		if m.Partial {
			continue
		}
		inc += m.Income
		buy += m.Buy
	}
	if inc <= 0 {
		return 0, false
	}
	return float64(buy) / float64(inc), true
}

// WhatIf applies a scenario on top of the baseline 30-day forecast. Everything is cash flow
// of the user's own ledger plus the scenario's explicit numbers — no invented revenue.
func WhatIf(st State, now time.Time, in WhatIfInput, p TaxPolicy) (WhatIfResult, string) {
	td := Compute(st, now, p)
	res := WhatIfResult{Baseline: td.Forecast, Scenario: make([]ForecastPoint, len(td.Forecast)), Assumptions: []string{}}
	delta := make([]float64, len(td.Forecast)) // cumulative adjustment per day (index 0 = tomorrow)
	addFrom := func(day int, v float64) {
		for i := max(day-1, 0); i < len(delta); i++ {
			delta[i] += v
		}
	}
	switch in.Kind {
	case "buy":
		pays := in.Payments
		if len(pays) == 0 {
			pays = []Payment{{Day: 1, Amount: -in.Amount}}
		}
		for _, py := range pays {
			if py.Day < 1 || py.Day > forecastDays {
				return res, "day_invalid"
			}
			addFrom(py.Day, float64(py.Amount))
		}
		res.Assumptions = append(res.Assumptions, "purchase paid on the given days; no extra sales assumed")
	case "helper":
		if in.Amount <= 0 {
			return res, "amount_required"
		}
		addFrom(forecastDays, -float64(in.Amount)) // first salary at the end of the window
		res.Assumptions = append(res.Assumptions, "first salary on day 30")
		if in.UpliftPct > 0 {
			cogs, ok := CogsShare(st, now)
			if !ok {
				cogs = 0.64
				res.Assumptions = append(res.Assumptions, "cost share 64% (no history yet)")
			}
			var income int64
			from := now.AddDate(0, 0, -30)
			for _, o := range st.Ops {
				if (o.Kind == KindSale || o.Kind == KindNas) && o.OccurredAt.After(from) && !o.OccurredAt.After(now) {
					income += o.Amount
				}
			}
			perDay := float64(income) / 30 * in.UpliftPct / 100 * (1 - cogs)
			for i := 10; i <= forecastDays; i++ {
				addFrom(i, perDay)
			}
			res.Assumptions = append(res.Assumptions, "extra sales from day 10, at last month's average, minus the cost share")
		}
	case "loan":
		if in.Amount <= 0 || in.TermMonths <= 0 {
			return res, "amount_required"
		}
		rate := in.AnnualRate
		if rate <= 0 {
			rate = 24
		}
		addFrom(1, float64(in.Amount))
		addFrom(forecastDays, -AnnuityPayment(float64(in.Amount), rate, in.TermMonths))
		res.Assumptions = append(res.Assumptions, "loan paid out on day 1, first annuity payment on day 30")
	default:
		return res, "kind_invalid"
	}
	low := 0
	for i, b := range res.Baseline {
		res.Scenario[i] = ForecastPoint{Date: b.Date, Balance: b.Balance + roundSom(delta[i])}
		if res.Scenario[i].Balance < res.Scenario[low].Balance {
			low = i
		}
	}
	res.Lowest, res.LowestDay = res.Scenario[low], low+1
	switch {
	case res.Lowest.Balance < 0:
		res.Verdict = "short"
	case res.Lowest.Balance < st.Business.Reserve:
		res.Verdict = "tight"
	default:
		res.Verdict = "ok"
	}
	return res, ""
}

// --- business plan ---

type PlanInput struct {
	Capital int64    `json:"capital"`
	Sales   int64    `json:"sales"` // extra monthly sales
	Rent    int64    `json:"rent"`
	Helper  int64    `json:"helper"`
	CogsPct *float64 `json:"cogs_pct"` // optional; default from the history
}

type PlanScenario struct {
	Profit        int64 `json:"profit"`
	PaybackMonths *int  `json:"payback_months"`
}

type PlanResult struct {
	CogsPct   float64      `json:"cogs_pct"`
	Estimated bool         `json:"estimated"` // true when no history: 64% is an assumption
	Base      PlanScenario `json:"base"`
	SalesDown PlanScenario `json:"sales_minus_20"`
	RentUp    PlanScenario `json:"rent_plus_20"`
}

func planScenario(in PlanInput, cogs, salesK, rentK float64) PlanScenario {
	profit := float64(in.Sales)*salesK*(1-cogs) - float64(in.Rent)*rentK - float64(in.Helper)
	sc := PlanScenario{Profit: roundSom(profit)}
	if profit > 0 {
		m := int(math.Ceil(float64(in.Capital) / profit))
		sc.PaybackMonths = &m
	}
	return sc
}

// Plan computes extra monthly profit and payback; the cost share comes from the user's own
// purchases / income unless given. Same formulas as the frontend's plan.ts.
func Plan(st State, now time.Time, in PlanInput) PlanResult {
	cogs, ok := CogsShare(st, now)
	res := PlanResult{Estimated: !ok}
	if !ok {
		cogs = 0.64
	}
	if in.CogsPct != nil {
		cogs, res.Estimated = *in.CogsPct/100, false
	}
	res.CogsPct = math.Round(cogs*1000) / 10
	res.Base = planScenario(in, cogs, 1, 1)
	res.SalesDown = planScenario(in, cogs, 0.8, 1)
	res.RentUp = planScenario(in, cogs, 1, 1.2)
	return res
}
