// Package prices collects market prices: the weekly averages the National Statistics Committee
// publishes on narx.stat.uz (public pages, robots.txt allows them; the source is credited),
// and anonymous medians from consenting users' purchases. The Source interface keeps further
// providers pluggable.
package prices

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Observation is one price at one place on one day.
type Observation struct {
	Product    string // normalized key
	Name       string
	Unit       string
	Market     string
	Price      int64
	ObservedAt time.Time
}

// Source produces observations; implementations must respect their provider's terms.
type Source interface {
	Name() string // stored in prices.source
	Fetch(ctx context.Context) ([]Observation, error)
}

// narxProducts are the food products on narx.stat.uz: page id → key, name, unit.
var narxProducts = []struct {
	ID        int
	Key, Name string
	Unit      string
}{
	{100, "guruch", "Guruch", "kg"}, {4001, "bugdoyunioliy", "Oliy navli bugʻdoy uni", "kg"}, {4002, "bugdoyuni1", "Birinchi navli bugʻdoy uni", "kg"},
	{114, "non", "Bugʻdoy unidan non", "kg"}, {1180, "molgoshti", "Mol goʻshti", "kg"}, {120, "qoygoshti", "Qoʻy goʻshti", "kg"},
	{1220, "tovuqgoshti", "Tovuq goʻshti", "kg"}, {1250, "baliq", "Baliq", "kg"}, {127, "sut", "Sut", "litr"}, {135, "tuxum", "Tuxum (10 dona)", "10 dona"},
	{1301, "paxtayogi", "Paxta yogʻi", "litr"}, {1302, "kungaboqaryogi", "Kungaboqar yogʻi", "litr"}, {138, "sariyog", "Sariyogʻ", "kg"},
	{140, "limon", "Limon", "kg"}, {141, "olma", "Olma", "kg"}, {142, "nok", "Nok", "kg"}, {150, "uzum", "Uzum", "kg"}, {156, "karam", "Karam", "kg"},
	{158, "bodring", "Bodring", "kg"}, {159, "pomidor", "Pomidor", "kg"}, {160, "baqlajon", "Baqlajon", "kg"}, {161, "bulgorqalampiri", "Bulgʻor qalampiri", "kg"},
	{164, "sabzi", "Sabzi", "kg"}, {166, "sarimsoq", "Sarimsoq", "kg"}, {167, "piyoz", "Piyoz", "kg"}, {170, "kartoshka", "Kartoshka", "kg"},
	{171, "loviya", "Loviya", "kg"}, {172, "noxat", "Noʻxat", "kg"}, {173, "mosh", "Moshʻ", "kg"}, {174, "shakar", "Shakar", "kg"},
}

type NarxStat struct {
	Base   string
	Client *http.Client
	Delay  time.Duration // pause between pages: be polite to a public service
}

func NewNarxStat() *NarxStat {
	return &NarxStat{Base: "https://narx.stat.uz", Client: &http.Client{Timeout: 20 * time.Second}, Delay: 2 * time.Second}
}

func (n *NarxStat) Name() string { return "narx_stat" }

var (
	tagRe    = regexp.MustCompile(`(?s)<script.*?</script>|<style.*?</style>`)
	anyTagRe = regexp.MustCompile(`<[^>]+>`)
	numRe    = regexp.MustCompile(`^\d{1,3}(?: \d{3})*$`)
	dateRe   = regexp.MustCompile(`Oxirgi kuzatuv (\d{2})\.(\d{2})\.(\d{4})`)
	regionRe = regexp.MustCompile(`(shahri|viloyati|Respublikasi)$`)
)

// ParsePage extracts the republic average and the regional prices from a product page.
// ok is false when the layout is not recognized (the caller logs it and moves on).
func ParsePage(page string, key, name, unit string) ([]Observation, bool) {
	text := anyTagRe.ReplaceAllString(tagRe.ReplaceAllString(page, ""), "\n")
	var lines []string
	for _, l := range strings.Split(html.UnescapeString(text), "\n") {
		if l = strings.TrimSpace(strings.ReplaceAll(l, " ", " ")); l != "" {
			lines = append(lines, l)
		}
	}
	m := dateRe.FindStringSubmatch(strings.Join(lines, "\n"))
	if m == nil {
		return nil, false
	}
	observed, err := time.Parse("02.01.2006", m[1]+"."+m[2]+"."+m[3])
	if err != nil {
		return nil, false
	}
	price := func(s string) int64 {
		v, _ := strconv.ParseInt(strings.ReplaceAll(s, " ", ""), 10, 64)
		return v
	}
	var out []Observation
	add := func(market string, p int64) {
		if p > 0 {
			out = append(out, Observation{Product: key, Name: name, Unit: unit, Market: market, Price: p, ObservedAt: observed})
		}
	}
	// Republic average: the number right before "so‘m / <unit>".
	for i := 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "so‘m /") && numRe.MatchString(lines[i-1]) {
			add("republic", price(lines[i-1]))
			break
		}
	}
	// Regions follow "Hududlar bo‘yicha" as "<region>\n<price>" pairs.
	start := -1
	for i, l := range lines {
		if l == "Hududlar bo‘yicha" {
			start = i
			break
		}
	}
	if start >= 0 {
		for i := start; i+1 < len(lines); i++ {
			if regionRe.MatchString(lines[i]) && numRe.MatchString(lines[i+1]) {
				if lines[i] == "Toshkent shahri" {
					add("toshkent_city", price(lines[i+1]))
				}
			}
		}
	}
	return out, len(out) > 0
}

func (n *NarxStat) get(ctx context.Context, id int) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/products/%d", n.Base, id), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "ConsulBot/1.0 (+https://consul.env.uz; price comparison for small traders; source credited)")
	resp, err := n.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("narx.stat.uz: http %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	return string(b), err
}

// Fetch reads every product page. A page that fails or has a new layout is skipped.
func (n *NarxStat) Fetch(ctx context.Context) ([]Observation, error) {
	var all []Observation
	var failed int
	for i, p := range narxProducts {
		if i > 0 {
			select {
			case <-ctx.Done():
				return all, ctx.Err()
			case <-time.After(n.Delay):
			}
		}
		page, err := n.get(ctx, p.ID)
		if err != nil {
			failed++
			continue
		}
		obs, ok := ParsePage(page, p.Key, p.Name, p.Unit)
		if !ok {
			failed++
			continue
		}
		all = append(all, obs...)
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("narx.stat.uz: no prices parsed (%d pages failed)", failed)
	}
	return all, nil
}
