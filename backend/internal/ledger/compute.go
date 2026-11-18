package ledger

import (
	"math"
	"sort"
	"time"
)

// TaxPolicy carries the tax rules the ledger needs. It is built in main from the calculator's
// rules file, so the ledger and the tax calculator share one source of truth without the
// modules importing each other.
type TaxPolicy struct {
	// Turnover returns turnover tax on revenue earned after ytdBefore in the same year
	// (brackets applied marginally). entity: ie | self | llc.
	Turnover func(entity string, ytdBefore, revenue float64) float64
	VAT6Pct  float64 // simplified VAT, % of turnover
	VAT12Pct float64 // VAT, % of (income − purchases) — a simplification for the dashboard
}

type Counterparty struct {
	Name  string
	Phone *string
}

// State is everything the computations need for one user.
type State struct {
	Ops            []Op // not deleted
	Obligations    []Obligation
	Business       Business
	Counterparties map[string]Counterparty // by id
}

const forecastDays = 30

func day(t time.Time) time.Time {
	t = t.In(Tashkent)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, Tashkent)
}

func monthKey(t time.Time) string { return t.In(Tashkent).Format("2006-01") }

func daysBetween(a, b time.Time) int { return int(math.Round(day(b).Sub(day(a)).Hours() / 24)) }

func roundSom(v float64) int64 { return int64(math.Round(v)) }

// pctOf returns part/whole in whole percent, rounded.
func pctOf(part, whole int64) int {
	if whole == 0 {
		return 0
	}
	return int(math.Round(float64(part) * 100 / float64(whole)))
}

func entityFor(status string) string {
	switch status {
	case "yatt":
		return "ie"
	case "oob":
		return "self"
	case "mchj":
		return "llc"
	}
	return "" // not registered: no tax is due
}

// cashDelta is the effect of an op on its method's balance.
func cashDelta(o Op) int64 {
	switch o.Kind {
	case KindSale, KindRepaid, KindOpening, KindFix:
		return o.Amount // fix is stored as a signed difference
	case KindBuy, KindExp, KindSup:
		return -o.Amount
	}
	return 0 // nas: income without cash
}

// after reports whether a happened after b in journal order.
func after(a, b Op) bool {
	if !a.OccurredAt.Equal(b.OccurredAt) {
		return a.OccurredAt.After(b.OccurredAt)
	}
	return a.CreatedAt.After(b.CreatedAt)
}

// Balance of one method. An `opening` op is a snapshot: the balance is the latest opening
// plus everything after it, so history entered for P&L before the snapshot does not double
// count cash.
func Balance(ops []Op, method string) int64 {
	var last *Op
	for i := range ops {
		if o := &ops[i]; o.Kind == KindOpening && o.Method == method && (last == nil || after(*o, *last)) {
			last = o
		}
	}
	var bal int64
	for _, o := range ops {
		if o.Method != method {
			continue
		}
		switch {
		case last == nil:
			bal += cashDelta(o)
		case o.ID == last.ID:
			bal += o.Amount
		case after(o, *last) && o.Kind != KindOpening:
			bal += cashDelta(o)
		}
	}
	return bal
}

type monthAgg struct{ income, buy, exp int64 }

func (m monthAgg) profit() int64 { return m.income - m.buy - m.exp }

func months(ops []Op) map[string]*monthAgg {
	out := map[string]*monthAgg{}
	for _, o := range ops {
		k := monthKey(o.OccurredAt)
		m := out[k]
		if m == nil {
			m = &monthAgg{}
			out[k] = m
		}
		switch o.Kind {
		case KindSale, KindNas:
			m.income += o.Amount
		case KindBuy, KindSup:
			m.buy += o.Amount
		case KindExp:
			m.exp += o.Amount
		}
	}
	return out
}

// MonthTax is the tax for the current month of now.
func MonthTax(st State, now time.Time, p TaxPolicy) int64 {
	ms := months(st.Ops)
	cur := ms[monthKey(now)]
	if cur == nil {
		return 0
	}
	switch st.Business.Regime {
	case "vat6":
		return roundSom(float64(cur.income) * p.VAT6Pct / 100)
	case "vat12":
		return roundSom(float64(max(cur.income-cur.buy, 0)) * p.VAT12Pct / 100)
	}
	entity := entityFor(st.Business.Status)
	if entity == "" || p.Turnover == nil {
		return 0
	}
	n := now.In(Tashkent)
	var ytdBefore int64
	for m := time.January; m < n.Month(); m++ {
		if agg := ms[time.Date(n.Year(), m, 1, 0, 0, 0, 0, Tashkent).Format("2006-01")]; agg != nil {
			ytdBefore += agg.income
		}
	}
	return roundSom(p.Turnover(entity, float64(ytdBefore), float64(cur.income)))
}

// nextDue returns the next due date of an obligation on or after today.
func nextDue(ob Obligation, today time.Time) (time.Time, bool) {
	if ob.DueDate != nil {
		d := day(*ob.DueDate)
		return d, !d.Before(today)
	}
	if ob.DueDay == nil {
		return time.Time{}, false
	}
	at := func(y int, m time.Month) time.Time {
		last := time.Date(y, m+1, 0, 0, 0, 0, 0, Tashkent).Day()
		return time.Date(y, m, min(*ob.DueDay, last), 0, 0, 0, 0, Tashkent)
	}
	d := at(today.Year(), today.Month())
	if d.Before(today) {
		d = at(today.Year(), today.Month()+1)
	}
	return d, true
}

// remaining of a one-off supplier obligation after `sup` payments to that supplier.
func remaining(ob Obligation, ops []Op) int64 {
	left := ob.Amount
	if ob.Kind != "supplier" || ob.Recurring || ob.CounterpartyID == nil {
		return left
	}
	for _, o := range ops {
		if o.Kind == KindSup && o.CounterpartyID != nil && *o.CounterpartyID == *ob.CounterpartyID && !o.OccurredAt.Before(ob.CreatedAt) {
			left -= o.Amount
		}
	}
	return max(left, 0)
}

// Debtors computes who owes what. Days count from the oldest nasiya not yet covered by
// repayments (FIFO).
func Debtors(st State, now time.Time) []Debtor {
	type acc struct {
		nas    []Op
		repaid int64
	}
	by := map[string]*acc{}
	for _, o := range st.Ops {
		if (o.Kind != KindNas && o.Kind != KindRepaid) || o.CounterpartyID == nil {
			continue
		}
		a := by[*o.CounterpartyID]
		if a == nil {
			a = &acc{}
			by[*o.CounterpartyID] = a
		}
		if o.Kind == KindNas {
			a.nas = append(a.nas, o)
		} else {
			a.repaid += o.Amount
		}
	}
	today := day(now)
	out := []Debtor{}
	for id, a := range by {
		sort.Slice(a.nas, func(i, j int) bool { return after(a.nas[j], a.nas[i]) })
		var total int64
		for _, o := range a.nas {
			total += o.Amount
		}
		owed := total - a.repaid
		if owed <= 0 {
			continue
		}
		paid, days := a.repaid, 0
		for _, o := range a.nas {
			if paid >= o.Amount {
				paid -= o.Amount
				continue
			}
			days = daysBetween(o.OccurredAt, today)
			break
		}
		cp := st.Counterparties[id]
		out = append(out, Debtor{ID: id, Name: cp.Name, Phone: cp.Phone, Owed: owed, Days: days})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Days != out[j].Days {
			return out[i].Days > out[j].Days
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Compute builds the "Today" screen.
//
//	cash        = naqd + karta
//	tax         = this month's turnover × rate (brackets by year-to-date turnover)
//	obligations = payments due within 30 days + tax
//	free        = cash − obligations − reserve
//	profit      = this month's income − purchases − expenses; profit_3m = average of 3 closed months
func Compute(st State, now time.Time, p TaxPolicy) Today {
	today := day(now)
	naqd, karta := Balance(st.Ops, MethodNaqd), Balance(st.Ops, MethodKarta)
	tax := MonthTax(st, now, p)

	t := Today{Upcoming: []Upcoming{}, Attention: []Attention{}, Forecast: []ForecastPoint{}, AsOf: now.In(Tashkent)}
	dueOn := map[string]int64{}
	var obligations int64
	for _, ob := range st.Obligations {
		due, ok := nextDue(ob, today)
		left := remaining(ob, st.Ops)
		if !ok || left <= 0 {
			continue
		}
		dl := daysBetween(today, due)
		if dl > forecastDays {
			continue
		}
		label := ob.Label
		if label == "" && ob.Counterparty != nil {
			label = *ob.Counterparty
		}
		t.Upcoming = append(t.Upcoming, Upcoming{Kind: ob.Kind, Label: label, Amount: left, DueDate: due.Format(time.DateOnly), DaysLeft: dl})
		obligations += left
		dueOn[due.Format(time.DateOnly)] += left
	}
	if tax > 0 {
		due := time.Date(today.Year(), today.Month()+1, 15, 0, 0, 0, 0, Tashkent)
		t.Upcoming = append(t.Upcoming, Upcoming{Kind: "tax", Label: "tax", Amount: tax, DueDate: due.Format(time.DateOnly), DaysLeft: daysBetween(today, due)})
		dueOn[due.Format(time.DateOnly)] += tax
	}
	obligations += tax
	sort.SliceStable(t.Upcoming, func(i, j int) bool { return t.Upcoming[i].DaysLeft < t.Upcoming[j].DaysLeft })

	ms := months(st.Ops)
	cur := ms[monthKey(now)]
	if cur == nil {
		cur = &monthAgg{}
	}
	prevKey := time.Date(today.Year(), today.Month()-1, 1, 0, 0, 0, 0, Tashkent).Format("2006-01")
	prev := ms[prevKey]
	if prev == nil {
		prev = &monthAgg{}
	}
	var sum3 int64
	n3 := 0
	for i := 1; i <= 3; i++ {
		if m := ms[time.Date(today.Year(), today.Month()-time.Month(i), 1, 0, 0, 0, 0, Tashkent).Format("2006-01")]; m != nil {
			sum3 += m.profit()
			n3++
		}
	}
	var profit3m int64
	if n3 > 0 {
		profit3m = roundSom(float64(sum3) / float64(n3))
	}

	debtors := Debtors(st, now)
	var owed int64
	for _, d := range debtors {
		owed += d.Owed
	}

	cash := naqd + karta
	t.View = View{
		Cash: cash, Naqd: naqd, Karta: karta, Tax: tax, Obligations: obligations,
		Reserve: st.Business.Reserve, Free: cash - obligations - st.Business.Reserve,
		Profit: cur.profit(), Margin: pctOf(cur.profit(), cur.income),
		OwedToMe: owed, DebtorCount: len(debtors), Profit3M: profit3m,
	}
	t.Month = MonthSummary{
		Month: monthKey(now), Income: cur.income, Buy: cur.buy, Exp: cur.exp,
		Profit: cur.profit(), Margin: pctOf(cur.profit(), cur.income),
		PrevIncome: prev.income, PrevProfit: prev.profit(), Partial: true,
	}

	// Attention, most urgent first.
	if t.View.Free < 0 {
		t.Attention = append(t.Attention, Attention{"free_negative", "alert", map[string]any{"free": t.View.Free}})
	} else if cash > 0 && t.View.Free*10 < cash {
		t.Attention = append(t.Attention, Attention{"free_low", "info", map[string]any{"free": t.View.Free}})
	}
	for _, u := range t.Upcoming {
		switch {
		case u.Kind == "tax" && u.DaysLeft <= 5:
			t.Attention = append(t.Attention, Attention{"tax_due", "warn", map[string]any{"amount": u.Amount, "due_date": u.DueDate, "days_left": u.DaysLeft}})
		case u.Kind != "tax" && u.DaysLeft <= 3:
			t.Attention = append(t.Attention, Attention{"payment_soon", "warn", map[string]any{"kind": u.Kind, "label": u.Label, "amount": u.Amount, "due_date": u.DueDate, "days_left": u.DaysLeft}})
		}
	}
	for _, d := range debtors {
		if d.Days >= 14 {
			sev := "warn"
			if d.Days >= 30 {
				sev = "alert"
			}
			t.Attention = append(t.Attention, Attention{"debtor_overdue", sev, map[string]any{"debtor_id": d.ID, "name": d.Name, "days": d.Days, "amount": d.Owed}})
		}
	}

	// Forecast: today's cash, plus the average daily net cash flow of the last 30 days,
	// minus each payment on its due date.
	from := now.AddDate(0, 0, -forecastDays)
	var net int64
	for _, o := range st.Ops {
		if o.OccurredAt.After(from) && !o.OccurredAt.After(now) && o.Kind != KindOpening && o.Kind != KindFix {
			net += cashDelta(o)
		}
	}
	daily := float64(net) / forecastDays
	bal := float64(cash)
	for i := 1; i <= forecastDays; i++ {
		d := today.AddDate(0, 0, i)
		bal += daily - float64(dueOn[d.Format(time.DateOnly)])
		t.Forecast = append(t.Forecast, ForecastPoint{Date: d.Format(time.DateOnly), Balance: roundSom(bal)})
	}
	return t
}
