package prices

import (
	"os"
	"testing"
	"time"
)

func TestParseRealNarxPage(t *testing.T) {
	b, err := os.ReadFile("testdata/narx_uzum.html") // a saved copy of https://narx.stat.uz/products/150
	if err != nil {
		t.Fatal(err)
	}
	obs, ok := ParsePage(string(b), "uzum", "Uzum", "kg")
	if !ok {
		t.Fatal("page not recognized")
	}
	got := map[string]int64{}
	for _, o := range obs {
		got[o.Market] = o.Price
		if !o.ObservedAt.Equal(time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)) || o.Product != "uzum" || o.Unit != "kg" {
			t.Errorf("observation = %+v", o)
		}
	}
	if got["republic"] != 13_545 || got["toshkent_city"] != 18_771 {
		t.Errorf("prices = %v, want republic 13 545 and Toshkent city 18 771", got)
	}
	if _, ok := ParsePage("<html>maintenance</html>", "x", "X", "kg"); ok {
		t.Error("an unknown layout must not produce prices")
	}
}
