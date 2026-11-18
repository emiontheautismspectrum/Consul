// Package kb is Consul's curated knowledge base: short definitions, the 2026 small-business
// tax rules the app uses and the state-support measures it links to, each with its source.
// Retrieval is lexical (stems of curated keywords), not vector search: every snippet is
// written and reviewed by people, so an answer can quote it and name its source. Entries
// marked verified=false (tax and support rules) must be shown with "check soliq.uz".
package kb

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strings"
	"unicode"
)

//go:embed kb.json
var raw []byte

type Source struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

type Entry struct {
	ID       string              `json:"id"`
	Verified *bool               `json:"verified"` // nil = general knowledge, false = rules to confirm
	KW       map[string][]string `json:"kw"`
	Title    map[string]string   `json:"title"`
	Text     map[string]string   `json:"text"`
	Sources  []Source            `json:"sources"`
}

type Hit struct {
	ID         string
	Title      string
	Text       string
	Sources    []Source
	Unverified bool // rules the user must confirm with the tax authority
	Score      int
}

type KB struct{ entries []Entry }

func Load() (*KB, error) {
	var es []Entry
	if err := json.Unmarshal(raw, &es); err != nil {
		return nil, err
	}
	return &KB{entries: es}, nil
}

var apostrophes = strings.NewReplacer("ʻ", "ʼ", "'", "ʼ", "’", "ʼ", "`", "ʼ", "ʻ", "ʼ")

func norm(s string) string {
	return apostrophes.Replace(strings.ToLower(s))
}

func tokens(s string) []string {
	return strings.FieldsFunc(norm(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != 'ʼ' && r != '%' })
}

// Search returns up to n entries ranked by how many curated keywords the query contains
// (single words match as stems at the start of a word; phrases match as substrings).
func (k *KB) Search(query, lang string, n int) []Hit {
	if lang != "ru" {
		lang = "uz"
	}
	q := norm(query)
	toks := tokens(query)
	var hits []Hit
	for _, e := range k.entries {
		score := 0
		for _, kw := range e.KW[lang] {
			kw = norm(kw)
			if strings.ContainsAny(kw, " 0123456789%") {
				if strings.Contains(q, strings.TrimSpace(kw)) {
					score += 4
				}
				continue
			}
			for _, t := range toks {
				if strings.HasPrefix(t, kw) {
					score += 3
					break
				}
			}
		}
		if score < 3 {
			continue
		}
		hits = append(hits, Hit{ID: e.ID, Title: e.Title[lang], Text: e.Text[lang], Sources: e.Sources,
			Unverified: e.Verified != nil && !*e.Verified, Score: score})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > n {
		hits = hits[:n]
	}
	return hits
}
