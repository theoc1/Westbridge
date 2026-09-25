package telephony_test

import (
	"errors"
	"testing"

	"github.com/dmalkin/westbridge/internal/telephony"
)

func TestNormalizeNumberAccepts(t *testing.T) {
	cases := map[string]string{
		"1002":               "1002",
		"  1002  ":           "1002",
		"+79991234567":       "+79991234567",
		"+7 (999) 123-45-67": "+79991234567",
		"8.800.555.35.35":    "88005553535",
	}
	for in, want := range cases {
		got, err := telephony.NormalizeNumber(in)
		if err != nil {
			t.Errorf("NormalizeNumber(%q): unexpected error %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizeNumber(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeNumberRejects(t *testing.T) {
	// Everything here is either meaningless or an attempt to escape the dial
	// string into another context or into extra dial options.
	bad := []string{
		"",
		"   ",
		"+",
		"()",
		"1002@evil-context",
		"1002/n",
		"1002,30,g",
		"1002;1003",
		"SIP/1002",
		"${EXTEN}",
		"1234567890123456789012345",
	}
	for _, in := range bad {
		got, err := telephony.NormalizeNumber(in)
		if err == nil {
			t.Errorf("NormalizeNumber(%q) = %q, want an error", in, got)
			continue
		}
		if !errors.Is(err, telephony.ErrInvalidNumber) {
			t.Errorf("NormalizeNumber(%q): error %v does not wrap ErrInvalidNumber", in, err)
		}
	}
}
