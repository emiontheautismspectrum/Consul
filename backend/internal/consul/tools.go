package consul

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"finadvisor/internal/calculator"
	"finadvisor/internal/infrastructure/llm"
	"finadvisor/internal/infrastructure/pii"
	"finadvisor/internal/ledger"
	"finadvisor/internal/ledger/parse"
)

func tool(name, desc, params string) llm.Tool {
	return llm.Tool{Type: "function", Function: llm.ToolFunction{Name: name, Description: desc, Parameters: json.RawMessage(params)}}
}

var toolDefs = []llm.Tool{
	tool("get_today", "The user's numbers today: cash (naqd, karta), what can be spent (free), obligations in 30 days, tax this month, reserve, profit and margin this month, average profit of 3 closed months, upcoming payments.", `{"type":"object","properties":{}}`),
	tool("get_months", "Income, purchases, expenses, profit and margin by month, oldest first; the current month is partial.", `{"type":"object","properties":{"months":{"type":"integer","minimum":1,"maximum":12}}}`),
	tool("list_debtors", "Customers who owe money (nasiya), as D1, D2…, with amount and days.", `{"type":"object","properties":{}}`),
	tool("safe_loan", "Largest loan whose monthly payment is at most 30% of the average monthly profit (3 closed months). Default 24% for 24 months.", `{"type":"object","properties":{"annual_rate":{"type":"number"},"term_months":{"type":"integer"}}}`),
	tool("calculate_loan", "Loan schedule summary for a given amount.", `{"type":"object","properties":{"amount":{"type":"number"},"annual_rate":{"type":"number"},"term_months":{"type":"integer"},"method":{"type":"string","enum":["annuity","differentiated"]}},"required":["amount","annual_rate","term_months"]}`),
	tool("calculate_tax", "Uzbekistan annual tax by regime for a given revenue.", `{"type":"object","properties":{"entity_type":{"type":"string","enum":["ie","self","llc"]},"annual_revenue":{"type":"number"},"annual_expenses":{"type":"number"},"regime":{"type":"string","enum":["auto","turnover","vat6","general"]}},"required":["annual_revenue"]}`),
	tool("calculate_breakeven", "Break-even and payback for an investment.", `{"type":"object","properties":{"initial_investment":{"type":"number"},"fixed_costs_monthly":{"type":"number"},"price_per_unit":{"type":"number"},"variable_cost_per_unit":{"type":"number"},"expected_units_monthly":{"type":"number"}},"required":["fixed_costs_monthly","price_per_unit","variable_cost_per_unit"]}`),
	tool("remember_fact", "Save a short fact about the user's business for later (only when the user explicitly asks to remember something: rent day, supplier, goal). The user can see and erase it.", `{"type":"object","properties":{"key":{"type":"string","description":"short label, e.g. Ijara, Taʼminotchi, Maqsad"},"value":{"type":"string","description":"the fact, one short sentence"}},"required":["key","value"]}`),
	tool("draft_operation", "Prepare (do NOT record) an operation the user asked to write down; the user confirms it in the app.", `{"type":"object","properties":{"kind":{"type":"string","enum":["sale","buy","exp","sup","nas","repaid"]},"amount":{"type":"integer"},"method":{"type":"string","enum":["naqd","karta","none"]},"label":{"type":"string"},"counterparty":{"type":"string"}},"required":["kind","amount"]}`),
}

// session is one request's tool context: the user's data and the debtor-name mask.
type session struct {
	ctx     context.Context
	engine  calculator.Engine
	state   ledger.State
	today   ledger.Today
	now     time.Time
	masks   map[string]string // D1 → real name
	debtors []ledger.Debtor
	pending *parse.Draft
	trace   []ToolTrace
	pool    []float64
	userID  string
	memory  MemoryStore
}

type ToolTrace struct {
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"arguments"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

func newSession(ctx context.Context, e calculator.Engine, st ledger.State, today ledger.Today, now time.Time) *session {
	s := &session{ctx: ctx, engine: e, state: st, today: today, now: now, masks: map[string]string{}}
	s.debtors = ledger.Debtors(st, now)
	for i, d := range s.debtors {
		s.masks[fmt.Sprintf("D%d", i+1)] = d.Name
	}
	return s
}

func (s *session) mask(name string) string {
	for k, v := range s.masks {
		if v == name {
			return k
		}
	}
	return name
}

// unmask restores debtor names in text the user will read ("D10" before "D1").
func (s *session) unmask(text string) string {
	for i := len(s.masks); i >= 1; i-- {
		k := fmt.Sprintf("D%d", i)
		text = strings.ReplaceAll(text, k, s.masks[k])
	}
	return text
}

// maskedToday is get_today without names: upcoming labels and attention params are masked.
func (s *session) maskedToday() any {
	t := s.today
	att := make([]ledger.Attention, 0, len(t.Attention))
	for _, a := range t.Attention {
		p := map[string]any{}
		for k, v := range a.Params {
			switch k {
			case "name":
				p[k] = s.mask(fmt.Sprint(v))
			case "debtor_id":
			default:
				p[k] = v
			}
		}
		att = append(att, ledger.Attention{Code: a.Code, Severity: a.Severity, Params: p})
	}
	return map[string]any{"view": t.View, "upcoming": t.Upcoming, "attention": att, "month": t.Month, "date": t.AsOf.Format(time.DateOnly)}
}

func (s *session) run(name, args string) (any, error) {
	switch name {
	case "get_today":
		return s.maskedToday(), nil
	case "get_months":
		var in struct{ Months int }
		_ = json.Unmarshal([]byte(args), &in)
		if in.Months < 1 || in.Months > 12 {
			in.Months = 6
		}
		return ledger.Months(s.state, s.now, in.Months), nil
	case "list_debtors":
		out := []map[string]any{}
		for i, d := range s.debtors {
			out = append(out, map[string]any{"id": fmt.Sprintf("D%d", i+1), "owed": d.Owed, "days": d.Days})
		}
		return out, nil
	case "safe_loan":
		var in struct {
			AnnualRate float64 `json:"annual_rate"`
			TermMonths int     `json:"term_months"`
		}
		_ = json.Unmarshal([]byte(args), &in)
		return safeLoan(s.ctx, s.engine, s.today.View.Profit3M, in.AnnualRate, in.TermMonths)
	case "calculate_loan":
		var in calculator.LoanInput
		if err := json.Unmarshal([]byte(args), &in); err != nil {
			return nil, err
		}
		r, err := s.engine.Loan(s.ctx, in)
		r.Schedule = nil
		return r, err
	case "calculate_tax":
		var in calculator.TaxInput
		if err := json.Unmarshal([]byte(args), &in); err != nil {
			return nil, err
		}
		return s.engine.Tax(s.ctx, in)
	case "calculate_breakeven":
		var in calculator.BreakevenInput
		if err := json.Unmarshal([]byte(args), &in); err != nil {
			return nil, err
		}
		r, err := s.engine.Breakeven(s.ctx, in)
		r.Cashflow = nil
		return r, err
	case "remember_fact":
		var in struct{ Key, Value string }
		if err := json.Unmarshal([]byte(args), &in); err != nil {
			return nil, err
		}
		// Personal data never goes into memory: names, phones, ids are masked first.
		in.Key, in.Value = strings.TrimSpace(pii.Mask(in.Key)), strings.TrimSpace(pii.Mask(in.Value))
		if in.Key == "" || in.Value == "" || len([]rune(in.Key)) > maxKeyRunes || len([]rune(in.Value)) > maxValueRunes {
			return nil, fmt.Errorf("key 1..%d and value 1..%d characters", maxKeyRunes, maxValueRunes)
		}
		if s.memory == nil {
			return nil, fmt.Errorf("memory is unavailable")
		}
		if err := s.memory.PutFact(s.ctx, s.userID, in.Key, in.Value); err != nil {
			return nil, err
		}
		return map[string]any{"saved": true, "key": in.Key, "value": in.Value}, nil
	case "draft_operation":
		var d parse.Draft
		if err := json.Unmarshal([]byte(args), &d); err != nil {
			return nil, err
		}
		switch d.Kind {
		case "sale", "buy", "exp", "sup", "nas", "repaid":
		default:
			return nil, fmt.Errorf("kind must be sale, buy, exp, sup, nas or repaid")
		}
		if d.Amount <= 0 || d.Amount > 10_000_000_000_000 {
			return nil, fmt.Errorf("amount must be a positive number of so'm")
		}
		if d.Method == "" {
			d.Method = "naqd"
		}
		if d.Kind == "nas" {
			d.Method = "none"
		}
		d.Counterparty = s.unmask(d.Counterparty)
		d.Source = "text"
		s.pending = &d
		return map[string]any{"draft": d, "recorded": false, "note": "the user must confirm it in the app"}, nil
	}
	return nil, fmt.Errorf("unknown tool %q", name)
}

// exec runs a tool call and returns the JSON for the model. Results and arguments feed the
// grounding pool.
func (s *session) exec(name, args string) string {
	tr := ToolTrace{Name: name, Args: json.RawMessage(args)}
	if !json.Valid([]byte(args)) {
		tr.Args, args = json.RawMessage(`{}`), "{}"
	}
	res, err := s.run(name, args)
	var out []byte
	if err != nil {
		tr.Error = err.Error()
		out, _ = json.Marshal(map[string]string{"error": err.Error()})
	} else {
		out, _ = json.Marshal(res)
		tr.Result = out
		var v any
		if json.Unmarshal(out, &v) == nil {
			collect(v, &s.pool)
		}
	}
	var a any
	if json.Unmarshal([]byte(args), &a) == nil {
		collect(a, &s.pool)
	}
	s.trace = append(s.trace, tr)
	return string(out)
}
