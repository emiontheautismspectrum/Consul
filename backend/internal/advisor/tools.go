package advisor

import (
	"context"
	"encoding/json"
	"fmt"

	"finadvisor/internal/calculator"
	"finadvisor/internal/infrastructure/llm"
)

// The LLM never does arithmetic itself: every figure in a plan must come from one of these
// deterministic calculators, invoked through tool calling.
var toolDefs = []llm.Tool{
	tool("calculate_loan",
		"Deterministic bank loan calculator (UZS). Returns monthly payment, total interest, overpayment. Use for any financing question.",
		`{"type":"object","properties":{
			"amount":{"type":"number","description":"Loan principal in UZS"},
			"annual_rate":{"type":"number","description":"Annual interest rate in percent, e.g. 24"},
			"term_months":{"type":"integer","description":"Term in months"},
			"method":{"type":"string","enum":["annuity","differentiated"]},
			"grace_months":{"type":"integer","description":"Interest-only months at the start, default 0"}},
		  "required":["amount","annual_rate","term_months","method"]}`),
	tool("calculate_tax",
		"Uzbekistan tax calculator (Tax Code rules, 2026). Computes taxes for turnover and general regimes, compares them and recommends one. Use for any tax question.",
		`{"type":"object","properties":{
			"entity_type":{"type":"string","enum":["llc","ie"],"description":"llc = legal entity (MChJ/OOO), ie = individual entrepreneur (YaTT)"},
			"regime":{"type":"string","enum":["auto","turnover","general"]},
			"annual_revenue":{"type":"number","description":"Annual revenue in UZS excluding VAT"},
			"annual_expenses":{"type":"number","description":"Annual deductible expenses excluding payroll, UZS"},
			"vatable_purchases":{"type":"number","description":"Annual purchases with input VAT, UZS"},
			"annual_payroll":{"type":"number","description":"Annual gross payroll, UZS"},
			"employees":{"type":"integer"}},
		  "required":["entity_type","annual_revenue"]}`),
	tool("calculate_breakeven",
		"Break-even and payback calculator. Returns break-even volume/revenue, monthly profit, payback months, 12-month ROI, 24-month cash flow.",
		`{"type":"object","properties":{
			"initial_investment":{"type":"number","description":"Startup investment, UZS"},
			"fixed_costs_monthly":{"type":"number","description":"Monthly fixed costs (rent, salaries incl. social tax, utilities), UZS"},
			"price_per_unit":{"type":"number"},
			"variable_cost_per_unit":{"type":"number"},
			"expected_units_monthly":{"type":"number"},
			"loan_payment_monthly":{"type":"number","description":"Monthly loan payment from calculate_loan, UZS"},
			"revenue_tax_pct":{"type":"number","description":"Tax on revenue in percent (turnover tax), from calculate_tax"}},
		  "required":["fixed_costs_monthly","price_per_unit","variable_cost_per_unit"]}`),
	tool("get_tax_rules",
		"Returns current Uzbekistan tax rates, thresholds and official sources (lex.uz, soliq.uz).",
		`{"type":"object","properties":{}}`),
}

func tool(name, desc, params string) llm.Tool {
	return llm.Tool{Type: "function", Function: llm.ToolFunction{Name: name, Description: desc, Parameters: json.RawMessage(params)}}
}

// ToolTrace records each calculator invocation; it is stored with the plan so the user
// (and the jury) can see exactly which numbers came from which calculation.
type ToolTrace struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// executeTool runs a calculator and returns a compact result for the model plus the full trace.
func executeTool(ctx context.Context, e calculator.Engine, name, args string) (string, ToolTrace) {
	tr := ToolTrace{Name: name, Arguments: json.RawMessage(args)}
	if !json.Valid([]byte(args)) {
		tr.Arguments = json.RawMessage(`{}`)
		tr.Error = "arguments are not valid JSON"
		return errJSON(tr.Error), tr
	}
	compact, err := runTool(ctx, e, name, args)
	if err != nil {
		tr.Error = err.Error()
		return errJSON(err.Error()), tr
	}
	b, _ := json.Marshal(compact)
	tr.Result = b
	return string(b), tr
}

func runTool(ctx context.Context, e calculator.Engine, name, args string) (any, error) {
	switch name {
	case "calculate_loan":
		var in calculator.LoanInput
		if err := json.Unmarshal([]byte(args), &in); err != nil {
			return nil, err
		}
		r, err := e.Loan(ctx, in)
		if err != nil {
			return nil, err
		}
		r.Schedule = nil // the summary is enough for the model and saves tokens
		return r, nil
	case "calculate_tax":
		var in calculator.TaxInput
		if err := json.Unmarshal([]byte(args), &in); err != nil {
			return nil, err
		}
		return e.Tax(ctx, in)
	case "calculate_breakeven":
		var in calculator.BreakevenInput
		if err := json.Unmarshal([]byte(args), &in); err != nil {
			return nil, err
		}
		r, err := e.Breakeven(ctx, in)
		if err != nil {
			return nil, err
		}
		r.Cashflow = sampleCashflow(r.Cashflow)
		return r, nil
	case "get_tax_rules":
		return e.Rules(), nil
	}
	return nil, fmt.Errorf("unknown tool %q", name)
}

func sampleCashflow(cf []calculator.CashflowPoint) []calculator.CashflowPoint {
	var out []calculator.CashflowPoint
	for _, p := range cf {
		switch p.Month {
		case 1, 3, 6, 12, 18, 24:
			out = append(out, p)
		}
	}
	return out
}

func errJSON(msg string) string {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return string(b)
}
