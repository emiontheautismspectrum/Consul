package calculator

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed rules/uz_tax_2026.json
var uzRulesJSON []byte

type Rate struct {
	Value   float64 `json:"value"`
	Article string  `json:"article"`
}

type Source struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

type TaxRules struct {
	Version           string          `json:"version"`
	EffectiveFrom     string          `json:"effective_from"`
	Currency          string          `json:"currency"`
	Verified          bool            `json:"verified"`
	Note              string          `json:"note"`
	TurnoverThreshold float64         `json:"turnover_threshold_annual"`
	BHMMonthly        float64         `json:"bhm_monthly"` // БРВ, base calculation amount
	Rates             map[string]Rate `json:"rates"`
	Sources           []Source        `json:"sources"`
}

func (r TaxRules) rate(k string) Rate {
	v, ok := r.Rates[k]
	if !ok {
		panic("tax rule missing: " + k)
	}
	return v
}

// LoadUZRules parses the embedded Uzbekistan tax rules. Rates are data, not code, so they
// can be updated after a legal change without touching the calculation logic.
func LoadUZRules() (TaxRules, error) {
	var r TaxRules
	if err := json.Unmarshal(uzRulesJSON, &r); err != nil {
		return r, fmt.Errorf("parse tax rules: %w", err)
	}
	for _, k := range []string{"turnover_tax_legal_entity_pct", "turnover_tax_individual_entrepreneur_pct",
		"vat_pct", "profit_tax_pct", "personal_income_tax_pct", "social_tax_pct",
		"ie_social_tax_bhm_per_month", "dividend_tax_pct"} {
		if _, ok := r.Rates[k]; !ok {
			return r, fmt.Errorf("tax rules: missing %s", k)
		}
	}
	return r, nil
}

const (
	EntityLLC = "llc" // юрлицо (ООО/MChJ)
	EntityIE  = "ie"  // индивидуальный предприниматель (ЯТТ)

	RegimeAuto     = "auto"
	RegimeTurnover = "turnover"
	RegimeGeneral  = "general"
)

type TaxInput struct {
	EntityType       string  `json:"entity_type"`       // llc | ie
	Regime           string  `json:"regime"`            // auto | turnover | general
	AnnualRevenue    float64 `json:"annual_revenue"`    // excluding VAT
	AnnualExpenses   float64 `json:"annual_expenses"`   // deductible costs excluding payroll, excluding VAT
	VATablePurchases float64 `json:"vatable_purchases"` // purchases with input VAT (general regime), excluding VAT
	AnnualPayroll    float64 `json:"annual_payroll"`    // gross salaries of employees
	Employees        int     `json:"employees"`         // informational
}

type TaxLine struct {
	Code    string  `json:"code"`
	Name    string  `json:"name"`
	Base    float64 `json:"base"`
	RatePct float64 `json:"rate_pct,omitempty"`
	Amount  float64 `json:"amount"`
	Article string  `json:"article"`
	Note    string  `json:"note,omitempty"`
}

type RegimeResult struct {
	Regime        string    `json:"regime"`
	Lines         []TaxLine `json:"lines"`
	TotalTax      float64   `json:"total_tax"`    // paid by the business incl. VAT (excl. withheld PIT)
	WithheldPIT   float64   `json:"withheld_pit"` // withheld from employees' salaries
	NetProfit     float64   `json:"net_profit"`
	EffectiveRate float64   `json:"effective_rate_pct"` // total_tax / revenue
}

type TaxResult struct {
	Input        TaxInput      `json:"input"`
	RulesVersion string        `json:"rules_version"`
	Selected     RegimeResult  `json:"selected"`
	Alternative  *RegimeResult `json:"alternative,omitempty"` // other eligible regime for comparison
	Recommended  string        `json:"recommended"`
	Warnings     []string      `json:"warnings,omitempty"`
	Sources      []Source      `json:"sources"`
}

func Tax(rules TaxRules, in TaxInput) (TaxResult, error) {
	if in.EntityType == "" {
		in.EntityType = EntityLLC
	}
	if in.Regime == "" {
		in.Regime = RegimeAuto
	}
	switch {
	case in.EntityType != EntityLLC && in.EntityType != EntityIE:
		return TaxResult{}, invalid("entity_type must be %q or %q", EntityLLC, EntityIE)
	case in.Regime != RegimeAuto && in.Regime != RegimeTurnover && in.Regime != RegimeGeneral:
		return TaxResult{}, invalid("regime must be auto, turnover or general")
	case in.AnnualRevenue < 0 || in.AnnualExpenses < 0 || in.AnnualPayroll < 0 || in.VATablePurchases < 0:
		return TaxResult{}, invalid("amounts must be >= 0")
	case in.Employees < 0:
		return TaxResult{}, invalid("employees must be >= 0")
	}

	res := TaxResult{Input: in, RulesVersion: rules.Version, Sources: rules.Sources}
	eligibleTurnover := in.AnnualRevenue <= rules.TurnoverThreshold
	regime := in.Regime
	if regime == RegimeAuto {
		regime = RegimeGeneral
		if eligibleTurnover {
			regime = RegimeTurnover
		}
	}
	if regime == RegimeTurnover && !eligibleTurnover {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"revenue exceeds %.0f UZS: turnover tax is not available, general regime (VAT + profit tax) applied",
			rules.TurnoverThreshold))
		regime = RegimeGeneral
	}

	res.Selected = calcRegime(rules, in, regime)
	if eligibleTurnover {
		alt := RegimeGeneral
		if regime == RegimeGeneral {
			alt = RegimeTurnover
		}
		a := calcRegime(rules, in, alt)
		res.Alternative = &a
	}
	res.Recommended = res.Selected.Regime
	// Compare by net profit: VAT is charged on top of the price and passed to the buyer,
	// so total tax alone would overstate the general regime's burden.
	if res.Alternative != nil && res.Alternative.NetProfit > res.Selected.NetProfit {
		res.Recommended = res.Alternative.Regime
	}
	if !rules.Verified {
		res.Warnings = append(res.Warnings, "tax rates are assumptions pending verification against lex.uz")
	}
	return res, nil
}

func calcRegime(rules TaxRules, in TaxInput, regime string) RegimeResult {
	rr := RegimeResult{Regime: regime}
	add := func(code, name string, base float64, rate Rate, note string) float64 {
		amt := round2(base * rate.Value / 100)
		rr.Lines = append(rr.Lines, TaxLine{code, name, round2(base), rate.Value, amt, rate.Article, note})
		return amt
	}

	var total float64
	// Payroll taxes apply in every regime.
	if in.AnnualPayroll > 0 {
		total += add("social_tax", "Социальный налог / Ijtimoiy soliq", in.AnnualPayroll, rules.rate("social_tax_pct"), "paid by employer")
		rr.WithheldPIT = add("pit", "НДФЛ сотрудников / JShDS", in.AnnualPayroll, rules.rate("personal_income_tax_pct"), "withheld from salaries, included in payroll")
	}
	if in.EntityType == EntityIE {
		r := rules.rate("ie_social_tax_bhm_per_month")
		amt := round2(r.Value * rules.BHMMonthly * 12)
		rr.Lines = append(rr.Lines, TaxLine{"ie_social_tax", "Соцналог ИП / YaTT ijtimoiy soliq", rules.BHMMonthly * 12, 0, amt, r.Article,
			fmt.Sprintf("%.0f BHM per month", r.Value)})
		total += amt
	}

	profitBeforeTax := in.AnnualRevenue - in.AnnualExpenses - in.AnnualPayroll - total
	switch regime {
	case RegimeTurnover:
		key := "turnover_tax_legal_entity_pct"
		if in.EntityType == EntityIE {
			key = "turnover_tax_individual_entrepreneur_pct"
		}
		t := add("turnover_tax", "Налог с оборота / Aylanmadan olinadigan soliq", in.AnnualRevenue, rules.rate(key), "")
		total += t
		profitBeforeTax -= t
	case RegimeGeneral:
		vatBase := max(in.AnnualRevenue-in.VATablePurchases, 0)
		total += add("vat", "НДС к уплате / QQS", vatBase, rules.rate("vat_pct"), "output VAT minus input VAT on purchases")
		if p := max(profitBeforeTax, 0); in.EntityType == EntityIE {
			t := add("ie_income_tax", "НДФЛ ИП / YaTT daromad solig'i", p, rules.rate("personal_income_tax_pct"), "")
			total += t
			profitBeforeTax -= t
		} else {
			t := add("profit_tax", "Налог на прибыль / Foyda solig'i", p, rules.rate("profit_tax_pct"), "")
			total += t
			profitBeforeTax -= t
		}
	}
	rr.TotalTax = round2(total)
	rr.NetProfit = round2(profitBeforeTax)
	if in.AnnualRevenue > 0 {
		rr.EffectiveRate = round2(rr.TotalTax / in.AnnualRevenue * 100)
	}
	return rr
}
