package parse

import "testing"

var known = []Known{{"Bekzod", "customer"}, {"Karim", "customer"}, {"Shoira opa", "customer"}, {"Akmal aka", "supplier"}}

func TestParse(t *testing.T) {
	cases := []struct {
		in                string
		typ, kind         string
		amount            int64
		method, label, cp string
		lang              string
	}{
		// sales
		{"bugun 1,2 mln sotdim", "op", "sale", 1_200_000, "naqd", "", "", "uz"},
		{"Bugun 1.2 mln sotdim", "op", "sale", 1_200_000, "naqd", "", "", "uz"},
		{"1 200 000 so'm sotdim", "op", "sale", 1_200_000, "naqd", "", "", "uz"},
		{"1200000 sotildi kartaga", "op", "sale", 1_200_000, "karta", "", "", "uz"},
		{"500 ming savdo bo'ldi", "op", "sale", 500_000, "naqd", "", "", "uz"},
		{"terminal orqali 2 mln tushdi", "op", "sale", 2_000_000, "karta", "", "", "uz"},
		{"сегодня продала на 1,5 млн", "op", "sale", 1_500_000, "naqd", "", "", "ru"},
		{"выручка 800 тыс наличными", "op", "sale", 800_000, "naqd", "", "", "ru"},
		{"продал на 300 тысяч по карте", "op", "sale", 300_000, "karta", "", "", "ru"},
		// purchases
		{"3 mln ga tovar oldim", "op", "buy", 3_000_000, "naqd", "", "", "uz"},
		{"Akmal akadan 5 mln xarid", "op", "buy", 5_000_000, "naqd", "", "Akmal aka", "uz"},
		{"закупила товар на 2 млн", "op", "buy", 2_000_000, "naqd", "", "", "ru"},
		// expenses with labels
		{"ijara uchun 3 mln to'ladim", "op", "exp", 3_000_000, "naqd", "ijara", "", "uz"},
		{"taksiga 50 ming ketdi", "op", "exp", 50_000, "naqd", "transport", "", "uz"},
		{"svet 200 ming to`ladim", "op", "exp", 200_000, "naqd", "kommunal", "", "uz"},
		{"yordamchiga oylik 2 mln berdim", "op", "exp", 2_000_000, "naqd", "ish haqi", "", "uz"},
		{"заплатила за аренду 3 млн", "op", "exp", 3_000_000, "naqd", "ijara", "", "ru"},
		{"такси 40 тыс", "op", "exp", 40_000, "naqd", "transport", "", "ru"},
		// supplier payment (known supplier + payment verb)
		{"Akmal akaga 3,1 mln to'ladim", "op", "sup", 3_100_000, "naqd", "", "Akmal aka", "uz"},
		// nasiya
		{"Bekzodga 500 ming nasiyaga berdim", "op", "nas", 500_000, "none", "", "Bekzod", "uz"},
		{"Dilshodga 300 ming qarzga", "op", "nas", 300_000, "none", "", "Dilshod", "uz"},
		{"Шоира опа в долг 700 тыс", "op", "nas", 700_000, "none", "", "Shoira opa", "ru"},
		// repaid
		{"Bekzod 500 ming qaytardi", "op", "repaid", 500_000, "naqd", "", "Bekzod", "uz"},
		{"Карим вернул 500 тыс на карту", "op", "repaid", 500_000, "karta", "", "Karim", "ru"},
		// fix (actual balance)
		{"kassada 5 mln bor", "fix", "fix", 5_000_000, "naqd", "", "", "uz"},
		{"kartada 7,2 mln qoldi", "fix", "fix", 7_200_000, "karta", "", "", "uz"},
		{"в кассе осталось 4 млн", "fix", "fix", 4_000_000, "naqd", "", "", "ru"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := Parse(c.in, known)
			if got.Type != c.typ || got.Lang != c.lang {
				t.Fatalf("type/lang = %s/%s, want %s/%s (%+v)", got.Type, got.Lang, c.typ, c.lang, got)
			}
			d := got.Draft
			if d == nil {
				t.Fatal("no draft")
			}
			if d.Kind != c.kind || d.Amount != c.amount || d.Method != c.method || d.Label != c.label || d.Counterparty != c.cp {
				t.Errorf("draft = %+v\nwant kind=%s amount=%d method=%s label=%q cp=%q", *d, c.kind, c.amount, c.method, c.label, c.cp)
			}
			if d.RawText == "" || d.Source != "text" {
				t.Errorf("raw_text/source missing: %+v", *d)
			}
		})
	}
}

func TestParseNonOps(t *testing.T) {
	cases := []struct{ in, typ, topic, missing string }{
		{"kredit olsam bo'ladimi?", "question", "loan", ""},
		{"34 mln kredit olsam qancha to'layman", "question", "loan", ""},
		{"soliq qancha?", "question", "tax", ""},
		{"bu oy foyda qancha", "question", "profit", ""},
		{"qancha pul sarflasam bo'ladi", "question", "spend", ""},
		{"сколько я могу потратить?", "question", "spend", ""},
		{"можно ли взять кредит", "question", "loan", ""},
		{"кто мне должен?", "question", "debtors", ""},
		{"kim menga qarz?", "question", "debtors", ""},
		{"qarz olsam bo'ladimi?", "question", "loan", ""},
		{"bank uchun hisobot", "report", "", ""},
		{"отчёт для банка", "report", "", ""},
		{"sotdim", "need_amount", "", "amount"},
		{"продала товар", "need_amount", "", "amount"},
		{"500 ming", "need_kind", "", "kind"},
		{"salom", "unknown", "", ""},
		{"", "unknown", "", ""},
	}
	for _, c := range cases {
		got := Parse(c.in, known)
		if got.Type != c.typ || got.Topic != c.topic {
			t.Errorf("%q → %s/%s, want %s/%s", c.in, got.Type, got.Topic, c.typ, c.topic)
		}
		if (c.missing == "") != (got.Missing == nil) || (got.Missing != nil && *got.Missing != c.missing) {
			t.Errorf("%q missing = %v, want %q", c.in, got.Missing, c.missing)
		}
	}
}

func TestRentAndConfirm(t *testing.T) {
	got := Parse("ijara har oyning 5-sanasida 3 mln", known)
	if got.Type != "rent" || got.Rent == nil || got.Rent.Amount != 3_000_000 || got.Rent.DueDay != 5 {
		t.Errorf("rent = %+v %+v", got, got.Rent)
	}
	got = Parse("аренда 3 млн каждый месяц 5 числа", known)
	if got.Type != "rent" || got.Rent.DueDay != 5 || got.Rent.Amount != 3_000_000 {
		t.Errorf("ru rent = %+v %+v", got, got.Rent)
	}
	if got := Parse("2 mlrd sotdim", known); !got.Confirm || got.Draft.Amount != 2_000_000_000 {
		t.Errorf("≥ 1 bln must ask to confirm: %+v", got)
	}
	if got := Parse("1,2 mln sotdim", known); got.Confirm {
		t.Error("small amount must not ask to confirm")
	}
}

func TestAmount(t *testing.T) {
	for in, want := range map[string]int64{
		"1,2 mln": 1_200_000, "1.5 млн": 1_500_000, "500 ming": 500_000, "500 тыс": 500_000, "2 mlrd": 2_000_000_000,
		"1 200 000": 1_200_000, "1200000 so'm": 1_200_000, "800k": 800_000, "3 миллиона": 3_000_000, "5-sanada 3 mln": 3_000_000,
	} {
		if got, ok := Amount(Norm(in)); !ok || got != want {
			t.Errorf("Amount(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"salom", "1,2", "5-sanada"} {
		if got, ok := Amount(Norm(in)); ok {
			t.Errorf("Amount(%q) = %d, want none", in, got)
		}
	}
}
