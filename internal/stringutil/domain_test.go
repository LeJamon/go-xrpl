package stringutil

import (
	"regexp"
	"strings"
	"testing"
)

// TestIsProperlyFormedTomlDomain_RegexCrossCheck pins the RE2 translation
// against an independent rippled-shaped regex (StringUtilities.cpp:142-153),
// so a future edit to tomlDomainRe that drifts from rippled's grammar fails
// here rather than silently.
func TestIsProperlyFormedTomlDomain_RegexCrossCheck(t *testing.T) {
	rippledLike := regexp.MustCompile(
		`^([A-Za-z0-9](?:[A-Za-z0-9\-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,63}$`,
	)
	check := func(s string) bool {
		if len(s) < 4 || len(s) > 128 {
			return false
		}
		return rippledLike.MatchString(s)
	}

	inputs := []string{
		"a.io", "example.com", "validator.example.com", "node-1.example.org",
		"a.b.c.d.example.com", "X-1.X-2.example.io",
		"localhost", "example", "a.b", "x.123", "a.b.c",
		"-bad.example.com", "bad-.example.com", "_bad.example.com",
		"example.com.", "example..com", ".example.com",
		"", "a",
		strings.Repeat("a", 63) + ".example.com",
		strings.Repeat("a", 64) + ".example.com",
		strings.Repeat("a", 200) + ".com",
		"foo.bar.MUSEUM",
	}
	for _, s := range inputs {
		want := check(s)
		got := IsProperlyFormedTomlDomain(s)
		if got != want {
			t.Errorf("IsProperlyFormedTomlDomain(%q): want=%v got=%v", s, want, got)
		}
	}
}

// TestIsProperlyFormedTomlDomain_TomlParity mirrors rippled's
// isProperlyFormedTomlDomain (StringUtilities.cpp:131-156), enumerating the
// length, label, and TLD boundaries.
func TestIsProperlyFormedTomlDomain_TomlParity(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"valid_two_label", "a.io", true},
		{"valid_subdomain", "validator.example.com", true},
		{"valid_with_digits", "node-1.example.org", true},
		{"valid_hyphenated", "my-validator.example.org", true},
		{"label_63_chars", strings.Repeat("a", 63) + ".example.com", true},
		{"too_short", "a.b", false},
		{"too_long", strings.Repeat("a", 130) + ".com", false},
		{"single_label", "example", false},
		{"numeric_tld", "x.123", false},
		{"one_char_tld", "a.b.c", false},
		{"trailing_dot", "example.com.", false},
		{"leading_dot", ".example.com", false},
		{"empty_label", "example..com", false},
		{"leading_hyphen_label", "-bad.example.com", false},
		{"trailing_hyphen_label", "bad-.example.com", false},
		{"label_64_chars", strings.Repeat("a", 64) + ".example.com", false},
		{"empty", "", false},
		{"underscore", "bad_label.example.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsProperlyFormedTomlDomain(tc.in); got != tc.want {
				t.Errorf("IsProperlyFormedTomlDomain(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
