package calculator

import (
	"errors"
	"strings"
	"testing"
)

func mustRules(t *testing.T) TaxRules {
	t.Helper()
	r, err := LoadUZRules()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func line(r RegimeResult, code string) *TaxLine {
	for i := range r.Lines {
		if r.Lines[i].Code == code {
			return &r.Lines[i]
		}
	}
	return nil
}

func TestTaxRulesHaveOfficialSources(t *testing.T) {
	r := mustRules(t)
	var lex, soliq bool
	for _, s := range r.Sources {
		lex = lex || strings.Contains(s.URL, "lex.uz")
		soliq = soliq || strings.Contains(s.URL, "soliq.uz")
	}
	if !lex || !soliq {
		t.Error("rules must reference lex.uz and soliq.uz")
	}
	for k, v := range r.Rates {
		if v.Article == "" {
			t.Errorf("rate %s has no legal reference", k)
		}
	}
}

func TestTaxTurnoverLLC(t *testing.T) {
	// LLC, 600M revenue, 300M expenses, 120M payroll → turnover tax 4% = 24M, social tax 12% = 14.4M.
	res, err := Tax(mustRules(t), TaxInput{EntityType: EntityLLC, AnnualRevenue: 600_000_000,
		AnnualExpenses: 300_000_000, AnnualPayroll: 120_000_000})
	if err != nil {
		t.Fatal(err)
	}
	s := res.Selected
	if s.Regime != RegimeTurnover {
		t.Fatalf("auto regime = %s, want turnover", s.Regime)
	}
	if l := line(s, "turnover_tax"); l == nil || l.Amount != 24_000_000 {
		t.Errorf("turnover tax line = %+v", l)
	}
	if l := line(s, "social_tax"); l == nil || l.Amount != 14_400_000 {
		t.Errorf("social tax line = %+v", l)
	}
	if s.WithheldPIT != 14_400_000 {
		t.Errorf("withheld PIT = %.2f", s.WithheldPIT)
	}
	if s.TotalTax != 38_400_000 || s.NetProfit != 141_600_000 { // 600−300−120−14.4−24
		t.Errorf("total %.2f net %.2f", s.TotalTax, s.NetProfit)
	}
	if res.Alternative == nil || res.Alternative.Regime != RegimeGeneral {
		t.Error("below threshold the general regime must be offered for comparison")
	}
	if len(res.Sources) == 0 {
		t.Error("result must carry sources")
	}
}

func TestTaxIEGets2026Rate(t *testing.T) {
	r := mustRules(t)
	res, err := Tax(r, TaxInput{EntityType: EntityIE, AnnualRevenue: 200_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if l := line(res.Selected, "turnover_tax"); l == nil || l.RatePct != 1 || l.Amount != 2_000_000 {
		t.Errorf("IE turnover line = %+v", l)
	}
	want := r.BHMMonthly * 12
	if l := line(res.Selected, "ie_social_tax"); l == nil || l.Amount != want {
		t.Errorf("IE social tax = %+v, want %.2f", l, want)
	}
}

func TestTaxAboveThresholdForcesGeneral(t *testing.T) {
	res, err := Tax(mustRules(t), TaxInput{EntityType: EntityLLC, Regime: RegimeTurnover,
		AnnualRevenue: 2_000_000_000, AnnualExpenses: 1_200_000_000, VATablePurchases: 1_000_000_000})
	if err != nil {
		t.Fatal(err)
	}
	s := res.Selected
	if s.Regime != RegimeGeneral || res.Alternative != nil {
		t.Fatalf("regime %s alt %v", s.Regime, res.Alternative)
	}
	if l := line(s, "vat"); l == nil || l.Amount != 120_000_000 { // 12% × (2bn − 1bn)
		t.Errorf("vat = %+v", l)
	}
	if l := line(s, "profit_tax"); l == nil || l.Amount != 120_000_000 { // 15% × 800M
		t.Errorf("profit tax = %+v", l)
	}
	found := false
	for _, w := range res.Warnings {
		found = found || strings.Contains(w, "turnover tax is not available")
	}
	if !found {
		t.Error("expected a threshold warning")
	}
}

func TestTaxGeneralLossHasNoProfitTax(t *testing.T) {
	res, _ := Tax(mustRules(t), TaxInput{Regime: RegimeGeneral, AnnualRevenue: 100_000_000, AnnualExpenses: 150_000_000})
	if l := line(res.Selected, "profit_tax"); l == nil || l.Amount != 0 {
		t.Errorf("profit tax on a loss = %+v", l)
	}
}

func TestTaxRecommendsHigherNetProfit(t *testing.T) {
	// Low-margin trade: turnover tax on full revenue hurts more than profit tax.
	res, _ := Tax(mustRules(t), TaxInput{AnnualRevenue: 900_000_000, AnnualExpenses: 870_000_000, VATablePurchases: 870_000_000})
	best := res.Selected
	if res.Alternative != nil && res.Alternative.NetProfit > best.NetProfit {
		best = *res.Alternative
	}
	if res.Recommended != best.Regime {
		t.Errorf("recommended %s, best by net profit is %s", res.Recommended, best.Regime)
	}
}

func TestTaxValidation(t *testing.T) {
	r := mustRules(t)
	for _, in := range []TaxInput{
		{EntityType: "corp"}, {Regime: "simplified"}, {AnnualRevenue: -1}, {Employees: -2},
	} {
		if _, err := Tax(r, in); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("Tax(%+v) err = %v", in, err)
		}
	}
}
