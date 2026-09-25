package telephony

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidNumber rejects numbers that cannot safely be dialled.
var ErrInvalidNumber = errors.New("conference: invalid number")

// maxNumberLen bounds a dialled number generously; E.164 tops out at 15
// digits, and internal dial plans occasionally use a few more.
const maxNumberLen = 24

// NormalizeNumber turns user input into something safe to interpolate into a
// dial string. Formatting characters a human might type are stripped, and what
// remains must be digits with at most a leading "+".
//
// This is a security boundary, not a convenience: the result ends up inside
// "Local/<number>@<context>", so a stray "@", "/", "," or ";" would let a
// caller redirect the call into another context or append dial options.
func NormalizeNumber(raw string) (string, error) {
	var b strings.Builder
	for _, r := range strings.TrimSpace(raw) {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && b.Len() == 0:
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '(' || r == ')' || r == '.':
			// Formatting noise from a human or a copy/paste; drop it.
		default:
			return "", fmt.Errorf("%w: %q contains unsupported character %q", ErrInvalidNumber, raw, r)
		}
	}

	number := b.String()
	digits := strings.TrimPrefix(number, "+")
	if digits == "" {
		return "", fmt.Errorf("%w: no digits in %q", ErrInvalidNumber, raw)
	}
	if len(number) > maxNumberLen {
		return "", fmt.Errorf("%w: %q is longer than %d characters", ErrInvalidNumber, raw, maxNumberLen)
	}
	return number, nil
}
