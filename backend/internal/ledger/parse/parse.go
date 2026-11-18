// Package parse understands short bookkeeping phrases in Uzbek (Latin) and Russian without
// an LLM: "bugun 1,2 mln sotdim", "Bekzodga 500 ming nasiyaga", "в кассе 5 млн". It never
// writes; the result is shown to the user for confirmation. Text the rules cannot place
// comes back as "unknown" and goes to the chat.
package parse

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const (
	TypeOp         = "op"
	TypeFix        = "fix"
	TypeRent       = "rent"
	TypeQuestion   = "question"
	TypeReport     = "report"
	TypeNeedAmount = "need_amount"
	TypeNeedKind   = "need_kind"
	TypeUnknown    = "unknown"

	// ConfirmThreshold: amounts this large are probably a typo (an extra "000") — ask first.
	ConfirmThreshold int64 = 1_000_000_000
)

// Known is an existing counterparty of the user, used to recognize suppliers and debtors.
type Known struct {
	Name string
	Kind string // customer | supplier
}

type Draft struct {
	Kind         string `json:"kind,omitempty"`
	Amount       int64  `json:"amount,omitempty"`
	Method       string `json:"method,omitempty"`
	Label        string `json:"label,omitempty"`
	Counterparty string `json:"counterparty,omitempty"`
	Source       string `json:"source"`
	RawText      string `json:"raw_text"`
}

type Rent struct {
	Amount int64 `json:"amount"`
	DueDay int   `json:"due_day,omitempty"`
}

type Intent struct {
	Type    string  `json:"type"`
	Draft   *Draft  `json:"draft,omitempty"`
	Rent    *Rent   `json:"rent,omitempty"`
	Topic   string  `json:"topic,omitempty"`
	Missing *string `json:"missing"`
	Lang    string  `json:"lang"`
	Confirm bool    `json:"confirm"`
}

// --- normalization ---

var apostrophes = strings.NewReplacer("ʻ", "'", "ʼ", "'", "’", "'", "‘", "'", "`", "'", "´", "'")

// Norm lowercases, unifies apostrophes and collapses spaces.
func Norm(s string) string {
	s = apostrophes.Replace(strings.ToLower(strings.TrimSpace(s)))
	return strings.Join(strings.Fields(s), " ")
}

func hasCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

// Lang: Cyrillic text is answered in Russian, everything else in Uzbek.
func Lang(s string) string {
	if hasCyrillic(s) {
		return "ru"
	}
	return "uz"
}

// --- amounts ---

// A number (1 200 000 | 1,2 | 1.5 | 500) optionally followed by a multiplier word.
var amountRe = regexp.MustCompile(`(\d{1,3}(?:[ \x{00a0}]\d{3})+|\d+(?:[.,]\d+)?)\s*(mlrd|milliard|млрд|миллиард\p{L}*|mln|million|millon|млн|миллион\p{L}*|ming|минг|тыс\p{L}*|т\.|k|m)?(?:\P{L}|$)`)

var multipliers = map[string]float64{
	"mlrd": 1e9, "milliard": 1e9, "млрд": 1e9,
	"mln": 1e6, "million": 1e6, "millon": 1e6, "млн": 1e6, "m": 1e6,
	"ming": 1e3, "минг": 1e3, "т.": 1e3, "k": 1e3,
}

func multiplier(w string) float64 {
	switch {
	case w == "":
		return 1
	case strings.HasPrefix(w, "миллиард"):
		return 1e9
	case strings.HasPrefix(w, "миллион"):
		return 1e6
	case strings.HasPrefix(w, "тыс"):
		return 1e3
	}
	return multipliers[w]
}

// Amount finds the first money amount in normalized text. Day numbers ("5-sanada",
// "5 числа") are not amounts.
func Amount(s string) (int64, bool) {
	for _, m := range amountRe.FindAllStringSubmatchIndex(s, -1) {
		num, unit := s[m[2]:m[3]], ""
		if m[4] >= 0 {
			unit = s[m[4]:m[5]]
		}
		rest := s[m[3]:]
		if unit == "" && (dayRe.MatchString(rest)) {
			continue
		}
		num = strings.NewReplacer(" ", "", " ", "").Replace(num)
		if strings.Count(num, ",")+strings.Count(num, ".") > 0 && unit == "" {
			// "1,2" without a unit is not money; "1.200.000" style is rare in chats.
			continue
		}
		f, err := strconv.ParseFloat(strings.ReplaceAll(num, ",", "."), 64)
		if err != nil {
			continue
		}
		v := int64(math.Round(f * multiplier(unit)))
		if v <= 0 {
			continue
		}
		return v, true
	}
	return 0, false
}

var dayRe = regexp.MustCompile(`^\s*-?\s*(sana|sanasi|chi|числа|число|го\b)`)

// dueDay finds "5-sanada", "har oyning 5", "5 числа".
var dueDayRe = regexp.MustCompile(`(\d{1,2})\s*-?\s*(?:sana|sanasi|chi|числа|число|го)|(?:har oyning|каждого)\s+(\d{1,2})`)

func dueDay(s string) int {
	m := dueDayRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	d, _ := strconv.Atoi(m[1] + m[2])
	if d < 1 || d > 31 {
		return 0
	}
	return d
}

// --- vocabulary ---

func has(s string, words ...string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func Method(s string) string {
	switch {
	case has(s, "karta", "kartaga", "kartadan", "plastik", "terminal", "click", "payme", "uzum", "перевод", "карт", "терминал"):
		return "karta"
	case has(s, "naqd", "kassa", "нал", "касс"):
		return "naqd"
	}
	return ""
}

var (
	nasWords    = []string{"nasiya", "qarzga", "qarziga", "в долг", "в рассрочку", "насия", "в кредит дал"}
	repaidWords = []string{"qaytardi", "qaytarib berdi", "qarzini berdi", "qarzini to'ladi", "вернул", "вернула", "отдал долг", "отдала долг", "погасил"}
	saleWords   = []string{"sotdim", "sotildi", "sotdik", "sotuv", "savdo", "tushum", "tushdi", "продал", "продала", "продали", "продажа", "продаж", "выручка", "выручки"}
	buyWords    = []string{"sotib oldim", "xarid", "oldim", "olib keldim", "keltirdim", "tovar oldim", "купил", "купила", "закупил", "закупила", "закупка", "закуп"}
	expWords    = []string{"to'ladim", "toladim", "xarajat", "sarfladim", "sarf", "ketdi", "berdim", "заплатил", "заплатила", "оплатил", "оплатила", "расход", "потратил", "потратила"}
	fixWords    = []string{" bor", " qoldi", "qoldi", "осталось", "остаток", "в кассе", "на карте", "kassada", "kartada"}
	reportWords = []string{"hisobot", "otchet", "отчёт", "отчет", "bank uchun", "для банка"}
	rentWords   = []string{"ijara", "аренд"}
	monthly     = []string{"har oy", "oyiga", "каждый месяц", "ежемесячно", "в месяц", "sanada", "sanasi", "числа", "число"}
)

// expense labels: Russian and Uzbek words map to one Uzbek label.
var expenseLabels = []struct {
	label string
	words []string
}{
	{"ijara", []string{"ijara", "аренд"}},
	{"transport", []string{"taksi", "benzin", "yo'l kira", "transport", "такси", "бензин", "доставк", "dostavka"}},
	{"kommunal", []string{"svet", "elektr", "gaz", "suv", "kommunal", "свет", "электр", "газ", "вода", "коммунал"}},
	{"ish haqi", []string{"ish haqi", "oylik", "yordamchi", "зарплат", "помощник", "работник"}},
	{"soliq", []string{"soliq", "налог"}},
	{"aloqa", []string{"internet", "telefon", "интернет", "телефон", "связь"}},
}

func expenseLabel(s string) string {
	for _, e := range expenseLabels {
		if has(s, e.words...) {
			return e.label
		}
	}
	return ""
}

var questionWords = []string{"qancha", "nechta", "nechchi", "bo'ladimi", "boladimi", "mumkinmi", "qanday", "nima ", "nimaga", "qachon", "olsam", "kerakmi",
	"сколько", "можно ли", "как ", "что ", "почему", "когда", "стоит ли", "хватит ли", "если возьму"}

var topics = []struct {
	topic string
	words []string
}{
	{"loan", []string{"kredit", "qarz olsam", "qarz olish", "кредит", "займ", "заём"}},
	{"tax", []string{"soliq", "налог"}},
	{"debtors", []string{"qarzdor", "nasiya", "qarz", "долж", "долг"}},
	{"spend", []string{"sarflasam", "sarflay", "bo'sh pul", "ishlatsam", "потратить", "можно потратить", "свободн"}},
	{"profit", []string{"foyda", "daromad", "прибыл", "заработ", "доход"}},
	{"plan", []string{"reja", "biznes reja", "ikkinchi rasta", "kengaytir", "план", "расшир", "второй"}},
	{"price", []string{"narx", "ustama", "цена", "наценк", "маржа"}},
}

func Topic(s string) string {
	for _, t := range topics {
		if has(s, t.words...) {
			return t.topic
		}
	}
	return "general"
}

func isQuestion(s string) bool { return strings.HasSuffix(s, "?") || has(s+" ", questionWords...) }

// --- counterparties ---

var nameStop = map[string]bool{}

func init() {
	for _, list := range [][]string{nasWords, repaidWords, saleWords, buyWords, expWords} {
		for _, w := range list {
			for _, f := range strings.Fields(w) {
				nameStop[f] = true
			}
		}
	}
	for _, w := range strings.Fields("bugun kecha hozir bu men biz so'm som sum mln ming mlrd naqd karta va ham to'liq hammasini " +
		"сегодня вчера сейчас я мы сум тыс млн млрд нал карта и всё весь долг деньги денег руб") {
		nameStop[w] = true
	}
}

// latinKey matches names across scripts and suffixes: "Бекзод", "bekzodga", "Bekzod".
func latinKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if l, ok := cyr[r]; ok {
			b.WriteString(l)
		} else if unicode.IsLetter(r) && !unicode.Is(unicode.Lm, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var cyr = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "yo", 'ж': "j", 'з': "z", 'и': "i", 'й': "y",
	'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f",
	'х': "x", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sh", 'ъ': "", 'ы': "i", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
	'ў': "o", 'қ': "q", 'ғ': "g", 'ҳ': "h",
}

// findKnown returns the known counterparty mentioned in the text (word starts with its key,
// so "akmal akaga" and "bekzoddan" match).
func findKnown(norm string, known []Known) (Known, bool) {
	words := strings.Fields(norm)
	for _, k := range known {
		first := strings.Fields(strings.ToLower(k.Name))
		if len(first) == 0 {
			continue
		}
		key := latinKey(first[0])
		if key == "" {
			continue
		}
		for _, w := range words {
			if strings.HasPrefix(latinKey(w), key) {
				return k, true
			}
		}
	}
	return Known{}, false
}

var uzCaseSuffix = regexp.MustCompile(`(ga|ka|qa|dan|ning|ni|ga\.|ga,)$`)

// guessName takes the first word of the original text that is not a keyword, number or unit,
// capitalized as typed, without Uzbek case suffixes ("Bekzodga" → "Bekzod").
func guessName(orig string) string {
	for _, raw := range strings.Fields(orig) {
		w := strings.Trim(apostrophes.Replace(raw), ".,!?:;\"'()")
		lw := strings.ToLower(w)
		if w == "" || nameStop[lw] || strings.IndexFunc(w, unicode.IsDigit) >= 0 || len([]rune(w)) < 2 {
			continue
		}
		if multiplier(lw) != 1 && multiplier(lw) != 0 {
			continue
		}
		skip := false
		for _, list := range [][]string{nasWords, repaidWords, saleWords, buyWords, expWords, {"so'm", "сум"}} {
			for _, k := range list {
				if strings.HasPrefix(lw, strings.Fields(k)[0]) {
					skip = true
				}
			}
		}
		if skip || Method(lw) != "" {
			continue
		}
		if !hasCyrillic(w) {
			w = uzCaseSuffix.ReplaceAllString(w, "")
		}
		if len([]rune(w)) < 2 {
			continue
		}
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		return string(r)
	}
	return ""
}

// --- intent ---

// Parse turns a phrase into an intent. known lists the user's counterparties.
func Parse(text string, known []Known) Intent {
	s := Norm(text)
	in := Intent{Type: TypeUnknown, Lang: Lang(text)}
	if s == "" {
		return in
	}
	amount, hasAmount := Amount(s)
	method := Method(s)
	need := func(what string) *string { return &what }
	draft := func(kind string) *Draft {
		d := &Draft{Kind: kind, Amount: amount, Method: method, Source: "text", RawText: strings.TrimSpace(text)}
		if d.Method == "" {
			d.Method = "naqd"
		}
		if kind == "nas" {
			d.Method = "none"
		}
		return d
	}
	finish := func(kind string, d *Draft) Intent {
		in.Type, in.Draft = TypeOp, d
		if !hasAmount {
			in.Type, in.Missing = TypeNeedAmount, need("amount")
		}
		in.Confirm = amount >= ConfirmThreshold
		return in
	}

	// Questions and reports first: "kredit olsam bo'ladimi?" must not become an op.
	if has(s, reportWords...) && !isQuestion(s) {
		in.Type = TypeReport
		return in
	}
	if isQuestion(s) {
		in.Type, in.Topic = TypeQuestion, Topic(s)
		return in
	}

	cp, hasKnown := findKnown(s, known)

	switch {
	case has(s, nasWords...):
		d := draft("nas")
		if hasKnown && cp.Kind == "customer" {
			d.Counterparty = cp.Name
		} else {
			d.Counterparty = guessName(text)
		}
		return finish("nas", d)
	case has(s, repaidWords...):
		d := draft("repaid")
		if hasKnown && cp.Kind == "customer" {
			d.Counterparty = cp.Name
		} else {
			d.Counterparty = guessName(text)
		}
		return finish("repaid", d)
	case has(s, rentWords...) && has(s, monthly...) && !has(s, "to'ladim", "toladim", "заплатил", "оплатил"):
		in.Type = TypeRent
		in.Rent = &Rent{Amount: amount, DueDay: dueDay(s)}
		if !hasAmount {
			in.Type, in.Missing = TypeNeedAmount, need("amount")
		}
		return in
	case has(s, saleWords...):
		return finish("sale", draft("sale"))
	case has(s, buyWords...):
		d := draft("buy")
		if hasKnown && cp.Kind == "supplier" {
			d.Counterparty = cp.Name
		}
		return finish("buy", d)
	case has(s, expWords...):
		if hasKnown && cp.Kind == "supplier" && expenseLabel(s) == "" {
			d := draft("sup")
			d.Counterparty = cp.Name
			return finish("sup", d)
		}
		d := draft("exp")
		d.Label = expenseLabel(s)
		return finish("exp", d)
	case hasAmount && method != "" && has(s, fixWords...):
		in.Type = TypeFix
		in.Draft = &Draft{Kind: "fix", Amount: amount, Method: method, Source: "text", RawText: strings.TrimSpace(text)}
		in.Confirm = amount >= ConfirmThreshold
		return in
	case hasAmount:
		if l := expenseLabel(s); l != "" {
			d := draft("exp")
			d.Label = l
			return finish("exp", d)
		}
		in.Type, in.Missing = TypeNeedKind, need("kind")
		in.Draft = &Draft{Amount: amount, Method: method, Source: "text", RawText: strings.TrimSpace(text)}
		in.Confirm = amount >= ConfirmThreshold
		return in
	}
	return in
}
