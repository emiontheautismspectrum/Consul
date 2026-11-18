// Package ledger is the bookkeeping module. The ops journal is the single source of truth:
// balances, debts, tax, profit and the "Today" screen are computed from it by the pure
// functions in compute.go; nothing derived is stored.
package ledger

import (
	"encoding/json"
	"time"
)

// Tashkent has no DST, so a fixed zone is exact and works in distroless images without tzdata.
var Tashkent = time.FixedZone("Asia/Tashkent", 5*60*60)

const (
	KindSale    = "sale"    // sale, paid now
	KindBuy     = "buy"     // purchase of goods
	KindExp     = "exp"     // other expense (rent, transport, salary…)
	KindSup     = "sup"     // payment to a supplier, reduces the supplier obligation
	KindNas     = "nas"     // sale on credit (nasiya): income now, cash later
	KindRepaid  = "repaid"  // debtor paid back: cash, not income
	KindFix     = "fix"     // balance correction, signed difference
	KindOpening = "opening" // starting balance snapshot for a method

	MethodNaqd  = "naqd"  // cash
	MethodKarta = "karta" // card
	MethodNone  = "none"
)

var (
	opKinds  = map[string]bool{KindSale: true, KindBuy: true, KindExp: true, KindSup: true, KindNas: true, KindRepaid: true, KindFix: true, KindOpening: true}
	sectors  = map[string]bool{"dried_fruit": true, "food": true, "clothes": true, "services": true, "other": true}
	markets  = map[string]bool{"chorsu": true, "oloy": true, "parkent": true, "quyliq": true, "other": true}
	statuses = map[string]bool{"yatt": true, "oob": true, "mchj": true, "none": true}
	regimes  = map[string]bool{"turnover": true, "vat6": true, "vat12": true}
)

type Item struct {
	Name  string  `json:"name"`
	Qty   float64 `json:"qty,omitempty"`
	Price int64   `json:"price,omitempty"`
}

type Op struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"`
	Amount         int64           `json:"amount"`
	Method         string          `json:"method"`
	Label          string          `json:"label"`
	CounterpartyID *string         `json:"counterparty_id"`
	Counterparty   *string         `json:"counterparty"`
	Fiscal         bool            `json:"fiscal"`
	Source         string          `json:"source"`
	Items          json.RawMessage `json:"items,omitempty"`
	OccurredAt     time.Time       `json:"occurred_at"`
	CreatedAt      time.Time       `json:"-"`
}

type Obligation struct {
	ID             string     `json:"id"`
	Kind           string     `json:"kind"`
	Label          string     `json:"label"`
	Amount         int64      `json:"amount"`
	DueDay         *int       `json:"due_day,omitempty"`
	DueDate        *time.Time `json:"-"`
	Recurring      bool       `json:"recurring"`
	CounterpartyID *string    `json:"-"`
	Counterparty   *string    `json:"counterparty,omitempty"`
	CreatedAt      time.Time  `json:"-"`
}

type Business struct {
	Trade   string `json:"trade"`
	Sector  string `json:"sector"`
	Market  string `json:"market"`
	Status  string `json:"status"`
	Regime  string `json:"regime"`
	Reserve int64  `json:"reserve"`
}

// DefaultBusiness is used until the user fills the business profile.
func DefaultBusiness() Business {
	return Business{Sector: "other", Market: "other", Status: "yatt", Regime: "turnover"}
}

type View struct {
	Cash        int64 `json:"cash"`
	Naqd        int64 `json:"naqd"`
	Karta       int64 `json:"karta"`
	Tax         int64 `json:"tax"`
	Obligations int64 `json:"obligations"`
	Reserve     int64 `json:"reserve"`
	Free        int64 `json:"free"`
	Profit      int64 `json:"profit"`
	Margin      int   `json:"margin"`
	OwedToMe    int64 `json:"owed_to_me"`
	DebtorCount int   `json:"debtor_count"`
	Profit3M    int64 `json:"profit_3m"`
}

type Upcoming struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Amount   int64  `json:"amount"`
	DueDate  string `json:"due_date"`
	DaysLeft int    `json:"days_left"`
}

type Attention struct {
	Code     string         `json:"code"`
	Severity string         `json:"severity"`
	Params   map[string]any `json:"params"`
}

type ForecastPoint struct {
	Date    string `json:"date"`
	Balance int64  `json:"balance"`
}

type MonthSummary struct {
	Month      string `json:"month"`
	Income     int64  `json:"income"`
	Buy        int64  `json:"buy"`
	Exp        int64  `json:"exp"`
	Profit     int64  `json:"profit"`
	Margin     int    `json:"margin"`
	PrevIncome int64  `json:"prev_income"`
	PrevProfit int64  `json:"prev_profit"`
	Partial    bool   `json:"partial"`
}

type Today struct {
	View      View            `json:"view"`
	Upcoming  []Upcoming      `json:"upcoming"`
	Attention []Attention     `json:"attention"`
	Forecast  []ForecastPoint `json:"forecast"`
	Month     MonthSummary    `json:"month"`
	AsOf      time.Time       `json:"as_of"`
}

type Debtor struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Phone *string `json:"phone"`
	Owed  int64   `json:"owed"`
	Days  int     `json:"days"`
}
