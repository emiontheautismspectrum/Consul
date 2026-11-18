package vision

import (
	"regexp"
	"strconv"
	"strings"
)

// DraftOp is an operation proposed from a document, for the user to confirm; nothing is
// recorded until POST /ops. Same shape as ledger.OpInput.
// LineItem is one position of a receipt; UnitPrice feeds the anonymous market medians.
type LineItem struct {
	Name      string  `json:"name"`
	Qty       float64 `json:"qty"`
	Unit      string  `json:"unit"`
	UnitPrice int64   `json:"unit_price"`
}

// DraftItem is the item shape stored with the operation (read by the prices module).
type DraftItem struct {
	Name  string  `json:"name"`
	Qty   float64 `json:"qty,omitempty"`
	Unit  string  `json:"unit,omitempty"`
	Price int64   `json:"price"`
}

type DraftOp struct {
	Kind         string      `json:"kind"`
	Amount       int64       `json:"amount"`
	Method       string      `json:"method"`
	Label        string      `json:"label,omitempty"`
	Counterparty string      `json:"counterparty,omitempty"`
	Fiscal       bool        `json:"fiscal"`
	Source       string      `json:"source"`
	Items        []DraftItem `json:"items,omitempty"`
}

// Documents that mean "we bought goods or paid for something".
var purchaseDocs = map[string]bool{"receipt": true, "invoice": true, "waybill": true, "act": true, "payment_order": true}

var (
	totalKeys    = []string{"total_amount", "total", "grand_total", "amount_due", "jami", "itogo", "summa"}
	supplierKeys = []string{"supplier", "seller", "vendor", "merchant", "store", "sotuvchi", "yetkazib_beruvchi"}
	// 14 000 000 | 14,000,000 | 14.000.000 | 1500000 — decimals (",00") are dropped.
	amountRe = regexp.MustCompile(`\d{1,3}(?:[ \x{00a0}.,]\d{3})+|\d+`)
	fiscalRe = regexp.MustCompile(`(?i)(qr|fiscal|фискал|fiskal|чек №|chek №|терминал|terminal|ккм|ккт)`)
	cardRe   = regexp.MustCompile(`(?i)(karta|kartadan|card|карт|uzcard|humo|visa|click|payme|терминал|terminal)`)
)

func fieldValue(fields []Field, keys []string) string {
	for _, k := range keys {
		for _, f := range fields {
			if strings.EqualFold(f.Key, k) && strings.TrimSpace(f.Value) != "" {
				return strings.TrimSpace(f.Value)
			}
		}
	}
	return ""
}

// parseAmount reads the first money amount in "14 000 000 сум" style text.
func parseAmount(s string) int64 {
	m := amountRe.FindString(s)
	if m == "" {
		return 0
	}
	digits := strings.NewReplacer(" ", "", " ", "", ".", "", ",", "").Replace(m)
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n <= 0 || n > 10_000_000_000_000 {
		return 0
	}
	return n
}

// BuildDrafts turns a recognized purchase document into at most one draft: the total as a
// purchase from the supplier, paid in cash unless the text mentions a card, marked fiscal
// when a receipt/QR marker is present. It adds a warning when the amount is unreadable.
func BuildDrafts(r *Result) []DraftOp {
	if !r.IsDocument || !purchaseDocs[r.DocumentType] {
		return nil
	}
	total := parseAmount(fieldValue(r.Fields, totalKeys))
	if total == 0 {
		r.Warnings = append(r.Warnings, "total amount not found: enter the purchase manually")
		return nil
	}
	method := "naqd"
	if cardRe.MatchString(r.Text) {
		method = "karta"
	}
	label := strings.TrimSpace(r.Title)
	if rs := []rune(label); len(rs) > 60 {
		label = string(rs[:60])
	}
	var items []DraftItem
	for _, it := range r.Items {
		name := strings.TrimSpace(it.Name)
		if name == "" || it.UnitPrice <= 0 || it.UnitPrice > 10_000_000_000 || len(items) >= 30 {
			continue
		}
		items = append(items, DraftItem{Name: name, Qty: it.Qty, Unit: strings.TrimSpace(it.Unit), Price: it.UnitPrice})
	}
	return []DraftOp{{
		Kind: "buy", Amount: total, Method: method, Label: label,
		Counterparty: fieldValue(r.Fields, supplierKeys), Fiscal: fiscalRe.MatchString(r.Text), Source: "photo", Items: items,
	}}
}
