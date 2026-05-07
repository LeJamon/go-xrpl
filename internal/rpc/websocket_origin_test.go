package rpc

import (
	"net/http"
	"testing"
)

// requestWithOrigin is a small helper to build a fake upgrade request with
// the given Origin header (empty string means no header at all).
func requestWithOrigin(origin string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "http://example.invalid/ws", nil)
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r
}

func TestCheckOrigin_EmptyAllowlistAllowsAll(t *testing.T) {
	check := makeCheckOrigin(nil)

	cases := []string{
		"",
		"http://example.com",
		"https://attacker.example",
		"not a url at all",
	}
	for _, origin := range cases {
		if !check(requestWithOrigin(origin)) {
			t.Errorf("empty allowlist should accept origin %q", origin)
		}
	}
}

func TestCheckOrigin_WildcardAllowsAll(t *testing.T) {
	check := makeCheckOrigin([]string{"*"})

	if !check(requestWithOrigin("https://anything.example")) {
		t.Fatal("wildcard allowlist must accept any origin")
	}
}

func TestCheckOrigin_AllowlistMatchesHost(t *testing.T) {
	check := makeCheckOrigin([]string{"example.com", "trusted.org:8443"})

	allowed := []string{
		"https://example.com",
		"https://EXAMPLE.com", // case-insensitive host
		"http://trusted.org:8443",
		"https://example.com:443", // bare-host fallback match
	}
	for _, origin := range allowed {
		if !check(requestWithOrigin(origin)) {
			t.Errorf("expected allowlist to accept origin %q", origin)
		}
	}

	rejected := []string{
		"https://attacker.example",
		"https://evil.example.com.attacker.test",
		"http://trusted.org:9999", // wrong port
	}
	for _, origin := range rejected {
		if check(requestWithOrigin(origin)) {
			t.Errorf("expected allowlist to reject origin %q", origin)
		}
	}
}

func TestCheckOrigin_MalformedOriginRejectedWhenAllowlistSet(t *testing.T) {
	check := makeCheckOrigin([]string{"example.com"})

	bad := []string{
		"://no-scheme",
		"https://",    // missing host
		"not-a-url",   // no scheme/host
		"http://[bad", // invalid URL (unterminated bracket)
	}
	for _, origin := range bad {
		if check(requestWithOrigin(origin)) {
			t.Errorf("expected malformed origin %q to be rejected", origin)
		}
	}
}

func TestCheckOrigin_MissingOriginAllowedWhenAllowlistSet(t *testing.T) {
	// Non-browser clients (curl, native xrpl.js in Node) do not send Origin.
	// A configured allowlist should not lock them out — Origin enforcement
	// only matters for browser-originated requests.
	check := makeCheckOrigin([]string{"example.com"})
	if !check(requestWithOrigin("")) {
		t.Fatal("requests without Origin header should be allowed even when an allowlist is set")
	}
}

func TestCheckOrigin_BlankAndWhitespaceEntriesIgnored(t *testing.T) {
	check := makeCheckOrigin([]string{"", "  ", "example.com"})

	if !check(requestWithOrigin("https://example.com")) {
		t.Error("blank entries should not affect a valid match")
	}
	if check(requestWithOrigin("https://other.example")) {
		t.Error("blank entries should not turn the allowlist into allow-all")
	}
}
