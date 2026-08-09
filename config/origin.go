package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// NormalizeOrigins validates and canonicalizes HTTP(S) origin values. Origins
// are compared as scheme plus host (including an explicit port); paths,
// credentials, queries, fragments, wildcards, and opaque URLs are rejected.
func NormalizeOrigins(origins []string) ([]string, error) {
	if len(origins) == 0 {
		return nil, nil
	}

	normalized := make([]string, 0, len(origins))
	seen := make(map[string]struct{}, len(origins))
	for _, raw := range origins {
		origin := strings.TrimSpace(raw)
		if origin == "" {
			return nil, fmt.Errorf("allowed_origins contains an empty origin")
		}
		canonical, err := CanonicalOrigin(origin)
		if err != nil {
			return nil, fmt.Errorf("allowed_origins entry %q: %w", raw, err)
		}
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		normalized = append(normalized, canonical)
	}
	return normalized, nil
}

// CanonicalOrigin validates one HTTP(S) origin and returns its canonical
// scheme://host[:port] form for exact comparisons.
func CanonicalOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return "", fmt.Errorf("origin scheme must be http or https")
	}
	if u.Opaque != "" || u.User != nil || u.Host == "" || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || strings.ContainsAny(raw, "?#") {
		return "", fmt.Errorf("origin must contain only scheme and host[:port]")
	}
	if strings.TrimSpace(u.Host) != u.Host || u.Hostname() == "" {
		return "", fmt.Errorf("origin host is invalid")
	}
	if strings.HasSuffix(u.Host, ":") {
		return "", fmt.Errorf("origin host is invalid")
	}
	if strings.HasPrefix(u.Host, "[") {
		closeBracket := strings.IndexByte(u.Host, ']')
		if closeBracket < 0 || (len(u.Host) > closeBracket+1 && (u.Host[closeBracket+1] != ':' || strings.Count(u.Host[closeBracket+1:], ":") > 1)) {
			return "", fmt.Errorf("origin host is invalid")
		}
	} else if strings.Count(u.Host, ":") > 1 {
		return "", fmt.Errorf("origin host is invalid")
	}
	if port := u.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", fmt.Errorf("origin port is invalid")
		}
	}
	if _, err := url.ParseRequestURI(raw); err != nil {
		return "", fmt.Errorf("origin is invalid: %w", err)
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), nil
}
