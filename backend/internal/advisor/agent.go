package advisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"finadvisor/internal/calculator"
	"finadvisor/internal/infrastructure/llm"
)

const maxIterations = 8

type Section struct {
	Key     string `json:"key"`
	Heading string `json:"heading"`
	Body    string `json:"body"`
}

type KeyNumber struct {
	Label    string  `json:"label"`
	Value    float64 `json:"value"`
	Unit     string  `json:"unit"`
	Tool     string  `json:"tool"`
	Verified bool    `json:"verified"` // value found in a calculator result
}

type Structured struct {
	Title       string      `json:"title"`
	Summary     string      `json:"summary"`
	Sections    []Section   `json:"sections"`
	KeyNumbers  []KeyNumber `json:"key_numbers"`
	Assumptions []string    `json:"assumptions"`
	Risks       []string    `json:"risks"`
	NextSteps   []string    `json:"next_steps"`
}

type AgentResult struct {
	Structured *Structured // nil when the model failed to return valid JSON
	Markdown   string
	Trace      []ToolTrace
	Usage      llm.Usage
}

var ErrNoToolUse = errors.New("model did not use calculators")

type Agent struct {
	llm       llm.Client
	engine    calculator.Engine
	maxTokens int
}

func NewAgent(c llm.Client, e calculator.Engine, maxTokens int) *Agent {
	return &Agent{llm: c, engine: e, maxTokens: maxTokens}
}

// Run drives the tool-calling loop: the model requests calculators, we execute them
// deterministically and feed results back until it produces the final JSON plan.
func (a *Agent) Run(ctx context.Context, lang, userPrompt string) (AgentResult, error) {
	var res AgentResult
	msgs := []llm.Message{
		{Role: "system", Content: systemPrompt(lang)},
		{Role: "user", Content: userPrompt},
	}
	nudged := false
	for i := 0; i < maxIterations; i++ {
		msg, usage, err := a.llm.Chat(ctx, llm.Request{Messages: msgs, Tools: toolDefs, MaxTokens: a.maxTokens})
		res.Usage.PromptTokens += usage.PromptTokens
		res.Usage.CompletionTokens += usage.CompletionTokens
		if err != nil {
			return res, err
		}
		msg.Role = "assistant"
		msgs = append(msgs, msg)

		if len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				out, tr := executeTool(ctx, a.engine, tc.Function.Name, tc.Function.Arguments)
				res.Trace = append(res.Trace, tr)
				msgs = append(msgs, llm.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: out})
			}
			continue
		}

		// Final answer without a single calculator call: ask once more, then give up.
		if len(res.Trace) == 0 {
			if nudged {
				return res, ErrNoToolUse
			}
			nudged = true
			msgs = append(msgs, llm.Message{Role: "user", Content: forceToolsMsg})
			continue
		}

		if s, ok := parseStructured(msg.Content); ok {
			return a.finish(res, s, lang), nil
		}
		// One repair attempt in JSON mode without tools.
		msgs = append(msgs, llm.Message{Role: "user", Content: forceJSONMsg})
		fix, usage, err := a.llm.Chat(ctx, llm.Request{Messages: msgs, JSONMode: true, MaxTokens: a.maxTokens})
		res.Usage.PromptTokens += usage.PromptTokens
		res.Usage.CompletionTokens += usage.CompletionTokens
		if err == nil {
			if s, ok := parseStructured(fix.Content); ok {
				return a.finish(res, s, lang), nil
			}
		}
		res.Markdown = strings.TrimSpace(msg.Content)
		return res, nil
	}
	return res, fmt.Errorf("agent: no final answer after %d iterations", maxIterations)
}

func (a *Agent) finish(res AgentResult, s *Structured, lang string) AgentResult {
	verifyNumbers(s, res.Trace)
	res.Structured = s
	res.Markdown = renderMarkdown(s, lang)
	return res
}

func parseStructured(content string) (*Structured, bool) {
	var s Structured
	if err := json.Unmarshal([]byte(llm.ExtractJSON(content)), &s); err != nil || s.Title == "" || len(s.Sections) == 0 {
		return nil, false
	}
	return &s, true
}

// verifyNumbers is a grounding check: every key number the model reports must match a
// value produced by a calculator (±0.5%, or equal after rounding to an integer).
func verifyNumbers(s *Structured, trace []ToolTrace) {
	var pool []float64
	for _, t := range trace {
		if len(t.Result) == 0 {
			continue
		}
		var v any
		if json.Unmarshal(t.Result, &v) == nil {
			collectNumbers(v, &pool)
		}
	}
	for i := range s.KeyNumbers {
		s.KeyNumbers[i].Verified = matches(s.KeyNumbers[i].Value, pool)
	}
}

func collectNumbers(v any, out *[]float64) {
	switch x := v.(type) {
	case float64:
		*out = append(*out, x)
	case map[string]any:
		for _, e := range x {
			collectNumbers(e, out)
		}
	case []any:
		for _, e := range x {
			collectNumbers(e, out)
		}
	}
}

func matches(v float64, pool []float64) bool {
	for _, p := range pool {
		if p == v || math.Round(p) == math.Round(v) {
			return true
		}
		if p != 0 && math.Abs(p-v)/math.Abs(p) <= 0.005 {
			return true
		}
	}
	return false
}

var mdHeadings = map[string][4]string{
	"uz": {"Asosiy ko'rsatkichlar", "Taxminlar", "Xatarlar", "Keyingi qadamlar"},
	"ru": {"Ключевые цифры", "Допущения", "Риски", "Следующие шаги"},
	"en": {"Key numbers", "Assumptions", "Risks", "Next steps"},
}

func renderMarkdown(s *Structured, lang string) string {
	h, ok := mdHeadings[lang]
	if !ok {
		h = mdHeadings["en"]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n%s\n", s.Title, s.Summary)
	for _, sec := range s.Sections {
		fmt.Fprintf(&b, "\n## %s\n\n%s\n", sec.Heading, sec.Body)
	}
	if len(s.KeyNumbers) > 0 {
		fmt.Fprintf(&b, "\n## %s\n\n| | | |\n|---|---:|---|\n", h[0])
		for _, n := range s.KeyNumbers {
			mark := "✓"
			if !n.Verified {
				mark = "⚠"
			}
			fmt.Fprintf(&b, "| %s | %s %s | %s %s |\n", n.Label, formatNum(n.Value), n.Unit, mark, n.Tool)
		}
	}
	list := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n## %s\n\n", title)
		for _, it := range items {
			fmt.Fprintf(&b, "- %s\n", it)
		}
	}
	list(h[1], s.Assumptions)
	list(h[2], s.Risks)
	list(h[3], s.NextSteps)
	return b.String()
}

// formatNum prints 12345678.9 as "12 345 678.90" (integers without decimals).
func formatNum(v float64) string {
	neg := v < 0
	v = math.Abs(v)
	intPart := int64(v)
	frac := math.Round((v - float64(intPart)) * 100)
	if frac == 100 {
		intPart, frac = intPart+1, 0
	}
	s := fmt.Sprintf("%d", intPart)
	var out []byte
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ' ')
		}
		out = append(out, s[i])
	}
	r := string(out)
	if frac != 0 {
		r += fmt.Sprintf(".%02d", int(frac))
	}
	if neg {
		r = "-" + r
	}
	return r
}
