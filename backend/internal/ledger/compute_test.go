package ledger

import (
	"fmt"
	"testing"
	"time"

	"finadvisor/internal/calculator"
)

func policy(t *testing.T) TaxPolicy {
	t.Helper()
	r, err := calculator.LoadUZRules()
	if err != nil {
		t.Fatal(err)
	}
	return TaxPolicy{Turnover: r.TurnoverTax, VAT6Pct: 6, VAT12Pct: 12}
}

func at(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, Tashkent) }

func ptr[T any](v T) *T { return &v }

var demoNow = at(2026, time.November, 18, 12)

type fixture struct {
	State
	seq int
}

func (f *fixture) op(kind string, amount int64, method string, when time.Time, cp ...string) {
	f.seq++
	o := Op{ID: fmt.Sprintf("op%d", f.seq), Kind: kind, Amount: amount, Method: method, OccurredAt: when, CreatedAt: when.Add(time.Duration(f.seq) * time.Millisecond)}
	if len(cp) > 0 {
		o.CounterpartyID = ptr(cp[0])
	}
	f.Ops = append(f.Ops, o)
}

// demo builds Dilnoza's reference ledger from the plan (today = 18 Nov 2026).
func demo() *fixture {
	f := &fixture{State: State{
		Business: Business{Trade: "quruq meva", Sector: "dried_fruit", Market: "chorsu", Status: "yatt", Regime: "turnover", Reserve: 1_200_000},
		Counterparties: map[string]Counterparty{
			"bekzod": {Name: "Bekzod"}, "karim": {Name: "Karim"}, "shoira": {Name: "Shoira opa"}, "akmal": {Name: "Akmal aka"},
		},
	}}
	// Three closed months, profit 6 000 000 each.
	for _, m := range []time.Month{time.August, time.September, time.October} {
		sale := int64(30_000_000)
		if m == time.October {
			sale -= 500_000
			f.op(KindNas, 500_000, MethodNone, at(2026, m, 28, 10), "bekzod") // 21 days before 18 Nov
		}
		f.op(KindSale, sale, MethodKarta, at(2026, m, 10, 10))
		f.op(KindBuy, 18_000_000, MethodKarta, at(2026, m, 5, 10))
		f.op(KindExp, 6_000_000, MethodNaqd, at(2026, m, 20, 10))
	}
	// Starting balances for November, then November's ops: income 50 000 000
	// (sales 48 800 000 + nasiya 1 200 000), purchases 32 000 000, expenses 12 000 000.
	f.op(KindOpening, 6_800_000, MethodNaqd, at(2026, time.November, 1, 0))
	f.op(KindOpening, 400_000, MethodKarta, at(2026, time.November, 1, 0))
	f.op(KindSale, 10_000_000, MethodNaqd, at(2026, time.November, 3, 10))
	f.op(KindSale, 38_800_000, MethodKarta, at(2026, time.November, 6, 10))
	f.op(KindNas, 500_000, MethodNone, at(2026, time.November, 4, 10), "karim")   // 14 days
	f.op(KindNas, 700_000, MethodNone, at(2026, time.November, 15, 10), "shoira") // 3 days
	f.op(KindBuy, 32_000_000, MethodKarta, at(2026, time.November, 7, 10))
	f.op(KindExp, 12_000_000, MethodNaqd, at(2026, time.November, 12, 10))

	f.Obligations = []Obligation{
		{ID: "rent", Kind: "rent", Label: "ijara", Amount: 3_000_000, DueDay: ptr(5), Recurring: true},
		{ID: "akmal", Kind: "supplier", Amount: 3_100_000, DueDate: ptr(at(2026, time.December, 10, 0)),
			CounterpartyID: ptr("akmal"), Counterparty: ptr("Akmal aka"), CreatedAt: at(2026, time.November, 7, 10)},
	}
	return f
}

func TestDemoReferenceNumbers(t *testing.T) {
	v := Compute(demo().State, demoNow, policy(t)).View
	want := View{Cash: 12_000_000, Naqd: 4_800_000, Karta: 7_200_000, Tax: 500_000, Obligations: 6_600_000,
		Reserve: 1_200_000, Free: 4_200_000, Profit: 6_000_000, Margin: 12, OwedToMe: 1_700_000, DebtorCount: 3, Profit3M: 6_000_000}
	if v != want {
		t.Errorf("view mismatch\n got: %+v\nwant: %+v", v, want)
	}
}

func TestSaleAndUndoMoveFree(t *testing.T) {
	f := demo()
	p := policy(t)
	f.op(KindSale, 1_200_000, MethodNaqd, demoNow)
	if got := Compute(f.State, demoNow, p).View.Free; got != 5_388_000 {
		t.Errorf("after sale free = %d, want 5 388 000 (+1 200 000 − 1%% tax)", got)
	}
	f.Ops = f.Ops[:len(f.Ops)-1] // undo = the op is no longer in the journal
	if got := Compute(f.State, demoNow, p).View.Free; got != 4_200_000 {
		t.Errorf("after undo free = %d, want 4 200 000", got)
	}
}

func TestDebtorsFIFO(t *testing.T) {
	ds := Debtors(demo().State, demoNow)
	got := map[string][2]int64{}
	for _, d := range ds {
		got[d.Name] = [2]int64{d.Owed, int64(d.Days)}
	}
	want := map[string][2]int64{"Bekzod": {500_000, 21}, "Karim": {500_000, 14}, "Shoira opa": {700_000, 3}}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s = %v, want %v", name, got[name], w)
		}
	}
	if ds[0].Name != "Bekzod" {
		t.Errorf("longest debt must come first, got %s", ds[0].Name)
	}

	// Partial repayment covers the oldest nasiya first: days move to the next one.
	f := demo()
	f.op(KindNas, 300_000, MethodNone, at(2026, time.November, 10, 10), "bekzod") // 8 days
	f.op(KindRepaid, 600_000, MethodNaqd, demoNow, "bekzod")
	for _, d := range Debtors(f.State, demoNow) {
		if d.Name == "Bekzod" && (d.Owed != 200_000 || d.Days != 8) {
			t.Errorf("Bekzod after partial repay = %d / %d days, want 200 000 / 8", d.Owed, d.Days)
		}
	}
	// Fully repaid debtors disappear; repayment moves cash, not income.
	f = demo()
	before := Compute(f.State, demoNow, policy(t))
	f.op(KindRepaid, 500_000, MethodNaqd, demoNow, "karim")
	after := Compute(f.State, demoNow, policy(t))
	if after.View.DebtorCount != 2 || after.View.Naqd != before.View.Naqd+500_000 || after.Month.Income != before.Month.Income {
		t.Errorf("repaid: debtors %d, naqd %d→%d, income %d→%d", after.View.DebtorCount, before.View.Naqd, after.View.Naqd, before.Month.Income, after.Month.Income)
	}
}

func TestUpcomingAndAttention(t *testing.T) {
	td := Compute(demo().State, demoNow, policy(t))
	want := []Upcoming{
		{Kind: "rent", Label: "ijara", Amount: 3_000_000, DueDate: "2026-12-05", DaysLeft: 17},
		{Kind: "supplier", Label: "Akmal aka", Amount: 3_100_000, DueDate: "2026-12-10", DaysLeft: 22},
		{Kind: "tax", Label: "tax", Amount: 500_000, DueDate: "2026-12-15", DaysLeft: 27},
	}
	if fmt.Sprint(td.Upcoming) != fmt.Sprint(want) {
		t.Errorf("upcoming\n got %+v\nwant %+v", td.Upcoming, want)
	}
	overdue := 0
	for _, a := range td.Attention {
		if a.Code == "debtor_overdue" {
			overdue++
		}
	}
	if overdue != 2 { // Bekzod 21 days, Karim 14 days; Shoira 3 days is fine
		t.Errorf("debtor_overdue = %d, want 2: %+v", overdue, td.Attention)
	}
	if len(td.Forecast) != 30 || td.Forecast[0].Date != "2026-11-19" {
		t.Errorf("forecast: %d points, first %+v", len(td.Forecast), td.Forecast[0])
	}
	if td.Month.PrevIncome != 30_000_000 || td.Month.PrevProfit != 6_000_000 || td.Month.Month != "2026-11" {
		t.Errorf("month = %+v", td.Month)
	}
}

func TestSupplierPaymentReducesObligation(t *testing.T) {
	f := demo()
	f.op(KindSup, 1_100_000, MethodKarta, demoNow, "akmal")
	td := Compute(f.State, demoNow, policy(t))
	for _, u := range td.Upcoming {
		if u.Kind == "supplier" && u.Amount != 2_000_000 {
			t.Errorf("supplier left = %d, want 2 000 000", u.Amount)
		}
	}
	// cash −1 100 000 and obligations −1 100 000: free unchanged
	if td.View.Free != 4_200_000 {
		t.Errorf("free = %d, want unchanged 4 200 000", td.View.Free)
	}
}

func TestOpeningIsASnapshotAndFixIsSigned(t *testing.T) {
	ops := []Op{
		{ID: "a", Kind: KindSale, Amount: 1_000, Method: MethodNaqd, OccurredAt: at(2026, 1, 1, 0)},
		{ID: "b", Kind: KindOpening, Amount: 5_000, Method: MethodNaqd, OccurredAt: at(2026, 2, 1, 0)},
		{ID: "c", Kind: KindExp, Amount: 300, Method: MethodNaqd, OccurredAt: at(2026, 2, 2, 0)},
		{ID: "d", Kind: KindFix, Amount: -200, Method: MethodNaqd, OccurredAt: at(2026, 2, 3, 0)},
		{ID: "e", Kind: KindSale, Amount: 7, Method: MethodKarta, OccurredAt: at(2026, 2, 3, 0)},
	}
	if got := Balance(ops, MethodNaqd); got != 4_500 { // 5000 − 300 − 200; the January sale predates the snapshot
		t.Errorf("naqd = %d, want 4 500", got)
	}
	if got := Balance(ops, MethodKarta); got != 7 {
		t.Errorf("karta = %d, want 7", got)
	}
}

func TestTaxBracketsYearToDate(t *testing.T) {
	p := policy(t)
	st := State{Business: Business{Status: "yatt", Regime: "turnover"}}
	// 990 mln earned before November, 20 mln in November: 10 mln at 1% + 10 mln at 4%.
	st.Ops = []Op{
		{ID: "1", Kind: KindSale, Amount: 990_000_000, Method: MethodKarta, OccurredAt: at(2026, time.March, 1, 10)},
		{ID: "2", Kind: KindSale, Amount: 20_000_000, Method: MethodKarta, OccurredAt: at(2026, time.November, 2, 10)},
	}
	if got := MonthTax(st, demoNow, p); got != 500_000 {
		t.Errorf("tax = %d, want 500 000", got)
	}
	st.Business.Status = "none"
	if got := MonthTax(st, demoNow, p); got != 0 {
		t.Errorf("unregistered tax = %d, want 0", got)
	}
	st.Business = Business{Status: "yatt", Regime: "vat6"}
	if got := MonthTax(st, demoNow, p); got != 1_200_000 {
		t.Errorf("vat6 tax = %d, want 1 200 000", got)
	}
}

func TestNameKey(t *testing.T) {
	for _, c := range [][2]string{{"Бекзод", "Bekzod"}, {"Шоира опа", "shoira opa"}, {"Gʻayrat", "g'ayrat"}, {"Ўткир", "O'tkir"}} {
		if NameKey(c[0]) != NameKey(c[1]) {
			t.Errorf("%q (%s) != %q (%s)", c[0], NameKey(c[0]), c[1], NameKey(c[1]))
		}
	}
}
