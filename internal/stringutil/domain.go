// Package stringutil holds low-level string helpers mirroring rippled's
// libxrpl/basics/StringUtilities.cpp.
package stringutil

import "regexp"

// tomlDomainRe validates xrpl.toml domains per rippled's grammar: dot-separated
// alphanumeric labels (hyphens allowed inside, not at the edges) ending in a TLD.
var tomlDomainRe = regexp.MustCompile(
	`^([A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,63}$`,
)

// IsProperlyFormedTomlDomain reports whether domain is a plausibly valid
// xrpl.toml domain, mirroring rippled's check in StringUtilities.cpp:131-156.
func IsProperlyFormedTomlDomain(domain string) bool {
	if len(domain) < 4 || len(domain) > 128 {
		return false
	}
	return tomlDomainRe.MatchString(domain)
}
