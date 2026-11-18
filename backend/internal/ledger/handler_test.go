package ledger

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestBuildOpValidation(t *testing.T) {
	now := at(2026, time.November, 18, 12)
	cases := []struct {
		name     string
		in       opInput
		balance  int64
		wantCode string // "" = expect success
		check    func(t *testing.T, o Op)
	}{
		{"bad kind", opInput{Kind: "steal", Amount: 1}, 0, "kind_invalid", nil},
		{"zero amount", opInput{Kind: KindSale}, 0, "amount_required", nil},
		{"negative amount", opInput{Kind: KindBuy, Amount: -5}, 0, "amount_required", nil},
		{"bad method", opInput{Kind: KindSale, Amount: 1, Method: "crypto"}, 0, "method_invalid", nil},
		{"nas needs a customer", opInput{Kind: KindNas, Amount: 100}, 0, "counterparty_required", nil},
		{"nas with cash method", opInput{Kind: KindNas, Amount: 100, Method: MethodNaqd, Counterparty: "Bekzod"}, 0, "method_invalid", nil},
		{"fix without method", opInput{Kind: KindFix, Amount: 100}, 0, "method_required", nil},
		{"bad source", opInput{Kind: KindSale, Amount: 1, Source: "carrier-pigeon"}, 0, "source_invalid", nil},
		{"bad items json", opInput{Kind: KindSale, Amount: 1, Items: []byte("{")}, 0, "items_invalid", nil},
		{"sale defaults naqd/manual", opInput{Kind: KindSale, Amount: 120}, 0, "", func(t *testing.T, o Op) {
			if o.Method != MethodNaqd || o.Source != "manual" || o.Amount != 120 || !o.OccurredAt.Equal(now) {
				t.Errorf("op = %+v", o)
			}
		}},
		{"nas defaults none", opInput{Kind: KindNas, Amount: 100, Counterparty: "Bekzod"}, 0, "", func(t *testing.T, o Op) {
			if o.Method != MethodNone {
				t.Errorf("method = %q, want none", o.Method)
			}
		}},
		// The user states the actual balance; the journal stores the signed difference.
		{"fix stores the difference", opInput{Kind: KindFix, Amount: 5_000, Method: MethodNaqd}, 3_200_000, "", func(t *testing.T, o Op) {
			if o.Amount != 5_000-3_200_000 {
				t.Errorf("fix amount = %d, want %d", o.Amount, 5_000-3_200_000)
			}
		}},
		{"negative fix input rejected", opInput{Kind: KindFix, Amount: -1, Method: MethodNaqd}, 0, "amount_required", nil},
		{"amount over 10 trillion", opInput{Kind: KindSale, Amount: 9_000_000_000_000_000_000}, 0, "amount_too_large", nil},
		{"date in 2099", opInput{Kind: KindSale, Amount: 1, OccurredAt: ptr(at(2099, 1, 1, 0))}, 0, "occurred_at_invalid", nil},
		{"date before 2000", opInput{Kind: KindSale, Amount: 1, OccurredAt: ptr(at(1999, 12, 31, 0))}, 0, "occurred_at_invalid", nil},
		{"counterparty too long", opInput{Kind: KindNas, Amount: 1, Counterparty: strings.Repeat("a", 101)}, 0, "counterparty_too_long", nil},
		{"counterparty without letters", opInput{Kind: KindNas, Amount: 1, Counterparty: "!!!"}, 0, "counterparty_invalid", nil},
		// Cutting by bytes would split "ш" and PostgreSQL rejects invalid UTF-8.
		{"label cut by characters", opInput{Kind: KindExp, Amount: 1, Label: "a" + strings.Repeat("ш", 300)}, 0, "", func(t *testing.T, o Op) {
			if !utf8.ValidString(o.Label) || utf8.RuneCountInString(o.Label) != 200 {
				t.Errorf("label: valid=%v runes=%d", utf8.ValidString(o.Label), utf8.RuneCountInString(o.Label))
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, err := buildOp(c.in, now, c.balance)
			if c.wantCode == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				c.check(t, o)
				return
			}
			var fe fieldErr
			if !errors.As(err, &fe) || fe.code != c.wantCode {
				t.Fatalf("err = %v, want code %s", err, c.wantCode)
			}
		})
	}
}

func TestCursorRoundTrip(t *testing.T) {
	c := Cursor{OccurredAt: at(2026, time.November, 18, 12).Add(123456789 * time.Nanosecond), ID: "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d"}
	got, err := DecodeCursor(c.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if !got.OccurredAt.Equal(c.OccurredAt) || got.ID != c.ID {
		t.Errorf("cursor = %+v, want %+v", got, c)
	}
	notUUID := Cursor{OccurredAt: c.OccurredAt, ID: "xyz"}.Encode()
	for _, bad := range []string{"", "!!!", "bm90LWEtY3Vyc29y", notUUID} {
		if _, err := DecodeCursor(bad); err == nil {
			t.Errorf("cursor %q decoded without error", bad)
		}
	}
}

func TestFormatSom(t *testing.T) {
	for in, want := range map[int64]string{
		0: "0", 42: "42", 1234: "1 234", 1_700_000: "1 700 000", -500_000: "-500 000",
	} {
		if got := formatSom(in); got != want {
			t.Errorf("formatSom(%d) = %q, want %q", in, got, want)
		}
	}
}
