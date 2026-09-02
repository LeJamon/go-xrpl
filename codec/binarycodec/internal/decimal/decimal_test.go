package decimal

import (
	"errors"
	"strconv"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		value string
		want  Parts
	}{
		{"0", Parts{}},
		{"+0.000e+80", Parts{}},
		{"-0e-80", Parts{Negative: true}},
		{"1", Parts{Mantissa: 1, RawMantissa: 1, Precision: 1}},
		{"+12.3400", Parts{Mantissa: 1234, Exponent: -2, RawMantissa: 123400, RawExponent: -4, Precision: 4}},
		{"-0.0012300e+4", Parts{Mantissa: 123, Exponent: -1, RawMantissa: 12300, RawExponent: -3, Negative: true, Precision: 3}},
		{"18446744073709551615", Parts{Mantissa: 18446744073709551615, RawMantissa: 18446744073709551615, Precision: 20}},
	}

	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			got, err := Parse(test.value)
			if err != nil {
				t.Fatalf("Parse(%q): %v", test.value, err)
			}
			if got != test.want {
				t.Fatalf("Parse(%q) = %+v, want %+v", test.value, got, test.want)
			}
		})
	}
}

func TestParseRejectsInvalidGrammarAndOverflow(t *testing.T) {
	for _, value := range []string{
		"", "+", "-", ".1", "1.", "001", "000.0", "1e", "1e+", "1e-",
		"junk1", "1junk", "1.2.3", "1e2e3", "1 2", "18446744073709551616",
		"1e2147483648", "1e-2147483648", "0.0000000000000000000018446744073709551616",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := Parse(value); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Parse(%q) error = %v, want ErrInvalid", value, err)
			}
		})
	}
}

func TestFormat(t *testing.T) {
	tests := []struct {
		mantissa uint64
		exponent int
		negative bool
		want     string
	}{
		{0, -96, true, "0"},
		{123, 2, false, "12300"},
		{123, -1, false, "12.3"},
		{123, -5, false, "0.00123"},
		{1_000_000_000_000_000, -15, false, "1"},
		{123, -1, true, "-12.3"},
	}

	for _, test := range tests {
		if got := Format(test.mantissa, test.exponent, test.negative); got != test.want {
			t.Errorf("Format(%d, %d, %t) = %q, want %q", test.mantissa, test.exponent, test.negative, got, test.want)
		}
	}
}

func FuzzParse(f *testing.F) {
	for _, value := range []string{
		"0", "+1", "-12.34e-5", "0.000", "001", ".1", "1.", "1e+2", "1e-2",
		"18446744073709551615", "18446744073709551616", "junk1junk",
	} {
		f.Add(value)
	}

	f.Fuzz(func(t *testing.T, value string) {
		parts, err := Parse(value)
		if err != nil {
			return
		}
		if parts.Mantissa == 0 {
			if parts.Precision != 0 {
				t.Fatalf("zero precision = %d", parts.Precision)
			}
			return
		}

		canonical := strconv.FormatUint(parts.Mantissa, 10)
		if parts.Exponent != 0 {
			canonical += "e" + strconv.Itoa(int(parts.Exponent))
		}
		if parts.Negative {
			canonical = "-" + canonical
		}
		reparsed, err := Parse(canonical)
		if err != nil {
			t.Fatalf("Parse(%q) after Parse(%q): %v", canonical, value, err)
		}
		if reparsed != parts {
			t.Fatalf("Parse(%q) = %+v after Parse(%q) = %+v", canonical, reparsed, value, parts)
		}
	})
}
