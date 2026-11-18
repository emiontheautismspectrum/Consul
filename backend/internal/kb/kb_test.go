package kb

import "testing"

func TestSearch(t *testing.T) {
	k, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ q, lang, want string }{
		{"stavka nima?", "uz", "rate"},
		{"что такое маржа", "ru", "margin"},
		{"marja va ustama farqi nima", "uz", "margin"}, // two entries: margin first or second, both returned
		{"1 mlrd dan oshsa soliq qancha", "uz", "tax_turnover"},
		{"что будет если оборот больше 1 млрд", "ru", "tax_turnover"},
		{"НДС какой", "ru", "tax_general"},
		{"garovsiz mikrokredit bormi", "uz", "support_micro"},
		{"что ты умеешь?", "ru", "capabilities"},
		{"Assalomu alaykum", "uz", "capabilities"},
		{"kafillik nima", "uz", "support_guarantee"},
	}
	for _, c := range cases {
		hits := k.Search(c.q, c.lang, 3)
		found := false
		for _, h := range hits {
			found = found || h.ID == c.want
		}
		if !found {
			t.Errorf("%q (%s): want %s in %+v", c.q, c.lang, c.want, hits)
		}
	}
	for _, q := range []string{"ob-havo qanday", "какая погода", ""} {
		if hits := k.Search(q, "uz", 3); len(hits) != 0 {
			t.Errorf("%q must match nothing, got %+v", q, hits)
		}
	}
	// Tax and support rules are flagged for confirmation; definitions are not.
	for _, h := range k.Search("QQS 12%", "uz", 3) {
		if h.ID == "tax_general" && !h.Unverified {
			t.Error("tax rules must be flagged unverified")
		}
	}
	for _, h := range k.Search("stavka", "uz", 1) {
		if h.Unverified {
			t.Error("a glossary entry is not a legal rule")
		}
	}
	for _, e := range k.entries {
		if e.Title["uz"] == "" || e.Title["ru"] == "" || e.Text["uz"] == "" || e.Text["ru"] == "" || len(e.KW["uz"]) == 0 || len(e.KW["ru"]) == 0 {
			t.Errorf("entry %s is incomplete", e.ID)
		}
		if e.Verified != nil && len(e.Sources) == 0 {
			t.Errorf("rule entry %s must name its source", e.ID)
		}
	}
}
