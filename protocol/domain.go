package protocol

import "regexp"

var tomlDomainRe = regexp.MustCompile(
	`^([A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,63}$`,
)

// IsProperlyFormedTomlDomain reports whether domain is a plausibly valid
// xrpl.toml domain.
func IsProperlyFormedTomlDomain(domain string) bool {
	if len(domain) < 4 || len(domain) > 128 {
		return false
	}
	return tomlDomainRe.MatchString(domain)
}
