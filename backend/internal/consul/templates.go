package consul

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"finadvisor/internal/calculator"
	"finadvisor/internal/ledger"
	"finadvisor/internal/ledger/parse"
)

//go:embed locales
var locales embed.FS

// tpl is one language's template file: section → key → text. "sug" and "kinds" are maps too.
type tpl struct {
	text map[string]map[string]any
}

func loadTemplates(lang string) (*tpl, error) {
	b, err := locales.ReadFile("locales/templates." + lang + ".json")
	if err != nil {
		return nil, err
	}
	t := &tpl{text: map[string]map[string]any{}}
	if err := json.Unmarshal(b, &t.text); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *tpl) s(section, key string) string {
	if v, ok := t.text[section][key].(string); ok {
		return v
	}
	return ""
}

func (t *tpl) list(section, key string) []string {
	raw, _ := t.text[section][key].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// fill replaces {name} placeholders.
func fill(s string, kv map[string]string) string {
	for k, v := range kv {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
}

// SafeLoan is the largest loan whose annuity payment is at most 30% of the average monthly
// profit of the last 3 closed months, rounded down to 100 000.
type SafeLoan struct {
	MaxAmount      int64   `json:"max_amount"`
	MonthlyPayment int64   `json:"monthly_payment"`
	Overpayment    int64   `json:"overpayment"`
	SharePct       int     `json:"share_of_profit_pct"`
	Profit3M       int64   `json:"profit_3m"`
	AnnualRate     float64 `json:"annual_rate"`
	TermMonths     int     `json:"term_months"`
}

const safeShare = 0.30

func safeLoan(ctx context.Context, e calculator.Engine, profit3m int64, rate float64, months int) (SafeLoan, error) {
	if rate <= 0 || rate > 100 {
		rate = 24
	}
	if months <= 0 || months > 120 {
		months = 24
	}
	out := SafeLoan{Profit3M: profit3m, AnnualRate: rate, TermMonths: months}
	if profit3m <= 0 {
		return out, nil
	}
	r := rate / 100 / 12
	cap := float64(profit3m) * safeShare
	amount := cap * (1 - math.Pow(1+r, -float64(months))) / r
	out.MaxAmount = int64(math.Floor(amount/100_000)) * 100_000
	if out.MaxAmount <= 0 {
		return out, nil
	}
	res, err := e.Loan(ctx, calculator.LoanInput{Amount: float64(out.MaxAmount), AnnualRate: rate, TermMonths: months, Method: calculator.MethodAnnuity})
	if err != nil {
		return out, err
	}
	out.MonthlyPayment = int64(math.Round(res.MonthlyPayment))
	out.Overpayment = int64(math.Round(res.TotalInterest))
	out.SharePct = int(math.Round(float64(out.MonthlyPayment) * 100 / float64(profit3m)))
	return out, nil
}

// facts is what templates may say; everything is computed by code.
type facts struct {
	today   ledger.Today
	debtors []ledger.Debtor
	loan    SafeLoan
	lang    string
}

func (t *tpl) opReply(in parse.Intent) Reply {
	r := Reply{Topic: "general"}
	switch in.Type {
	case parse.TypeNeedAmount:
		r.Text = t.s("need_amount", "text")
		ask := t.s("need_amount", "ask")
		r.Ask = &ask
	case parse.TypeNeedKind:
		r.Text = fill(t.s("need_kind", "text"), map[string]string{"amount": fmtSom(in.Draft.Amount)})
		r.Sug = t.list("need_kind", "sug")
	case parse.TypeRent:
		r.Text = fill(t.s("rent", "text"), map[string]string{"day": fmt.Sprint(in.Rent.DueDay), "amount": fmtSom(in.Rent.Amount)})
		r.Rent = in.Rent
		r.Actions = []Action{{Label: t.s("rent", "yes"), Run: "add_rent", Primary: true}}
	default: // op, fix
		d := *in.Draft
		kv := map[string]string{"kind": t.s("kinds", d.Kind), "amount": fmtSom(d.Amount), "method": t.s("methods", d.Method)}
		if d.Counterparty != "" {
			kv["kind"] += " · " + d.Counterparty
		}
		r.Text = fill(t.s("op_confirm", "text"), kv)
		if in.Confirm {
			r.Text = fill(t.s("op_confirm_big", "text"), kv)
		}
		r.PendingOp = &d
		r.Actions = []Action{{Label: t.s("op_confirm", "yes"), Run: "confirm_op", Primary: true}, {Label: t.s("op_confirm", "edit"), Run: "edit_op"}}
	}
	return r
}

// topicReply answers a topic from computed facts only.
func (t *tpl) topicReply(topic string, f facts) Reply {
	v := f.today.View
	r := Reply{Topic: topic, Sug: t.list("sug", topic)}
	som := fmtSom
	switch topic {
	case "spend":
		if v.Free < 0 {
			r.Text = fill(t.s("spend_negative", "text"), map[string]string{"gap": som(-v.Free)})
		} else {
			r.Text = fill(t.s("spend", "text"), map[string]string{"free": som(v.Free)})
		}
		r.Card = &Card{Title: t.s("spend", "title"), Value: som(v.Free), Rows: [][2]string{
			{t.s("spend", "cash"), som(v.Cash)}, {t.s("spend", "obligations"), "−" + som(v.Obligations)}, {t.s("spend", "reserve"), "−" + som(v.Reserve)}}}
		r.Actions = []Action{{Label: t.s("spend", "action"), Href: "/app/pul", Primary: true}}
	case "loan":
		l := f.loan
		if l.MaxAmount <= 0 {
			r.Text = t.s("loan_none", "text")
			r.Actions = []Action{{Label: t.s("loan_none", "action"), Href: "/app/pul"}}
			break
		}
		kv := map[string]string{"amount": som(l.MaxAmount), "months": fmt.Sprint(l.TermMonths), "payment": som(round1000(l.MonthlyPayment)),
			"share": fmt.Sprint(l.SharePct), "rate": fmt.Sprint(l.AnnualRate)}
		r.Text = fill(t.s("loan", "text"), kv)
		r.Card = &Card{Title: fill(t.s("loan", "title"), kv), Value: som(l.MaxAmount), Rows: [][2]string{
			{t.s("loan", "payment"), "~" + som(round1000(l.MonthlyPayment))}, {t.s("loan", "overpay"), som(l.Overpayment)}, {t.s("loan", "profit"), som(l.Profit3M)}}}
		ask := t.s("loan", "ask")
		r.Ask = &ask
		r.Work = []Work{{true, fill(t.s("loan", "work"), kv)}}
		r.Actions = []Action{{Label: t.s("loan", "action"), Href: "/app/hisob", Cx: "kredit", Primary: true}}
	case "tax":
		due := ""
		for _, u := range f.today.Upcoming {
			if u.Kind == "tax" {
				due = u.DueDate
			}
		}
		if due == "" {
			n := f.today.AsOf
			due = time.Date(n.Year(), n.Month()+1, 15, 0, 0, 0, 0, ledger.Tashkent).Format(time.DateOnly)
		}
		r.Text = fill(t.s("tax", "text"), map[string]string{"tax": som(v.Tax), "due": due})
		r.Card = &Card{Title: t.s("tax", "title"), Value: som(v.Tax), Rows: [][2]string{{t.s("tax", "turnover"), som(f.today.Month.Income)}}}
		r.Work = []Work{{true, t.s("tax", "work")}}
		r.Actions = []Action{{Label: t.s("tax", "action"), Href: "/app/soliq", Primary: true}}
	case "profit":
		m := f.today.Month
		r.Text = fill(t.s("profit", "text"), map[string]string{"profit": som(m.Profit), "margin": fmt.Sprint(m.Margin), "prev": som(m.PrevProfit)})
		r.Card = &Card{Title: t.s("profit", "title"), Value: som(m.Profit), Rows: [][2]string{
			{t.s("profit", "income"), som(m.Income)}, {t.s("profit", "costs"), "−" + som(m.Buy+m.Exp)}}}
		r.Actions = []Action{{Label: t.s("profit", "action"), Href: "/app/pul", Primary: true}}
	case "debtors":
		if len(f.debtors) == 0 {
			r.Text = t.s("debtors", "none")
			break
		}
		d := f.debtors[0]
		r.Text = fill(t.s("debtors", "text"), map[string]string{"count": fmt.Sprint(len(f.debtors)), "total": som(v.OwedToMe), "name": d.Name, "days": fmt.Sprint(d.Days)})
		rows := [][2]string{}
		for _, x := range f.debtors {
			rows = append(rows, [2]string{x.Name, som(x.Owed)})
		}
		r.Card = &Card{Title: t.s("debtors", "title"), Value: som(v.OwedToMe), Rows: rows}
		r.Actions = []Action{{Label: t.s("debtors", "action"), Href: "/app/nasiya", Primary: true}}
	case "plan":
		r.Text = fill(t.s("plan", "text"), map[string]string{"profit3m": som(v.Profit3M)})
		r.Actions = []Action{{Label: t.s("plan", "action"), Href: "/app/reja", Primary: true}}
	case "price":
		r.Text = t.s("price", "text")
		r.Actions = []Action{{Label: t.s("price", "action"), Href: "/app/hisob", Cx: "narx", Primary: true}}
	case "report":
		r.Text = t.s("report", "text")
		r.Actions = []Action{{Label: t.s("report", "action"), Run: "bankReport", Primary: true}}
		r.Topic = "general"
	default:
		r.Topic = "general"
		r.Text = t.s("general", "text")
		r.Sug = t.list("general", "sug")
	}
	return r
}
