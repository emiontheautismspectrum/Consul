package auth

import (
	"errors"
	"strings"
)

var ErrInvalidPhone = errors.New("invalid phone")

// NormalizePhone turns the ways Uzbek numbers are typed (+998 90 123-45-67, 998901234567,
// 90 123 45 67, (90) 1234567) into +998XXXXXXXXX. Anything else is ErrInvalidPhone, so
// formatting variants cannot be used to dodge per-phone rate limits.
func NormalizePhone(in string) (string, error) {
	var digits strings.Builder
	for i, r := range strings.TrimSpace(in) {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r == '+' && i == 0, r == ' ', r == '-', r == '(', r == ')':
		default:
			return "", ErrInvalidPhone
		}
	}
	d := digits.String()
	switch {
	case len(d) == 12 && strings.HasPrefix(d, "998"):
		d = d[3:]
	case len(d) == 9:
	default:
		return "", ErrInvalidPhone
	}
	if d[0] == '0' {
		return "", ErrInvalidPhone
	}
	return "+998" + d, nil
}

// MaskPhone keeps only what support needs to recognize a number: +998 *** ** 67.
func MaskPhone(p string) string {
	if len(p) < 4 {
		return "***"
	}
	return "+998***" + p[len(p)-4:len(p)-2] + " " + p[len(p)-2:]
}
