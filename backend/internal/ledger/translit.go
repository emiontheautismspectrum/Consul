package ledger

import (
	"strings"
	"unicode"
)

// cyrToLat follows the Uzbek Latin alphabet, so names typed in Cyrillic match Latin ones.
var cyrToLat = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "yo", 'ж': "j", 'з': "z",
	'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r",
	'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "x", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sh",
	'ъ': "", 'ы': "i", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
	'ў': "o", 'қ': "q", 'ғ': "g", 'ҳ': "h",
}

// Latin transliterates Cyrillic to Uzbek Latin and lowercases.
func Latin(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if l, ok := cyrToLat[r]; ok {
			b.WriteString(l)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// NameKey is the matching key for counterparties: Latin, lowercase, letters and digits only
// (apostrophes ʻ ' ’ ` and spaces dropped), so "Шоира опа", "Shoira opa" and "shoira-opa" match.
func NameKey(name string) string {
	var b strings.Builder
	for _, r := range Latin(name) {
		// ʻ and ʼ are modifier letters (Lm) in Unicode but apostrophes in Uzbek.
		if (unicode.IsLetter(r) && !unicode.Is(unicode.Lm, r)) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
