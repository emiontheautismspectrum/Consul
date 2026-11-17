package advisor

import (
	"regexp"
	"strings"
)

// Personal data is masked before anything leaves for the LLM provider. Patterns are tuned for
// Uzbekistan: +998 phones, STIR/INN (9 digits, only next to a keyword so plain amounts like
// 500000000 survive), PINFL (14 digits), passport series (AA1234567), bank cards.
var piiPatterns = []struct {
	re   *regexp.Regexp
	mask string
}{
	{regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`), "[EMAIL]"},
	{regexp.MustCompile(`\b(?:\d{4}[ \-]?){3}\d{4}\b`), "[CARD]"},
	{regexp.MustCompile(`\b\d{14}\b`), "[PINFL]"},
	{regexp.MustCompile(`(?i)(ИНН|СТИР|STIR|INN|TIN)\s*[:№#]?\s*\d{9}\b`), "$1 [TIN]"},
	{regexp.MustCompile(`(?:\+|\b)998[\s\-]?\(?\d{2}\)?[\s\-]?\d{3}[\s\-]?\d{2}[\s\-]?\d{2}\b`), "[PHONE]"},
	{regexp.MustCompile(`\b[A-Z]{2}\s?\d{7}\b`), "[PASSPORT]"},
}

// Local mobile numbers without the country code (90 123 45 67, 901234567). Round
// 9-digit amounts such as 500000000 look the same, so numbers ending in 00000 are kept.
var localPhone = regexp.MustCompile(`\b(?:33|50|55|77|88|90|91|93|94|95|97|98|99)[\s\-]?\d{3}[\s\-]?\d{2}[\s\-]?\d{2}\b`)

// Anonymize masks personal identifiers in free text.
func Anonymize(s string) string {
	for _, p := range piiPatterns {
		s = p.re.ReplaceAllString(s, p.mask)
	}
	return localPhone.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasSuffix(m, "00000") {
			return m
		}
		return "[PHONE]"
	})
}
