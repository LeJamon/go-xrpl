package peermanagement

import (
	"bufio"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProtocolVersionHeaderListRules(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		inbound  string
		outbound string
	}{
		{"quoted", `"XRPL/2.2"`, "XRPL/2.2", "XRPL/2.2"},
		{"quoted pair", `"XRPL\/2.2"`, "XRPL/2.2", "XRPL/2.2"},
		{"quoted comma", `"XRPL/2.2,XRPL/2.3"`, "", ""},
		{"escaped comma", `"XRPL/2.2\,XRPL/2.3"`, "", ""},
		{"escaped quote", `"XRPL/2.2\""`, "", ""},
		{"outside escape", `XRPL\/2.2`, "", ""},
		{"quoted whitespace", `" XRPL/2.2 "`, "", ""},
		{"quoted trailing whitespace", `"XRPL/2.2 "`, "", ""},
		{"linear whitespace", " X R P L /\t2 . 2 ", "XRPL/2.2", "XRPL/2.2"},
		{"leading non-ASCII whitespace", "\u00a0XRPL/2.2", "", ""},
		{"trailing non-ASCII whitespace", "XRPL/2.2\u2003", "", ""},
		{"leading control whitespace", "\vXRPL/2.2", "", ""},
		{"trailing control whitespace", "XRPL/2.2\r\n\v\f", "XRPL/2.2", "XRPL/2.2"},
		{"quoted control whitespace", "\"XRPL/2.2\r\"", "", ""},
		{"unclosed quote", `"XRPL/2.2`, "XRPL/2.2", "XRPL/2.2"},
		{"unclosed quoted pair", `"XRPL/2.2\`, "XRPL/2.2", "XRPL/2.2"},
		{"empty items", `,, "", XRPL/2.2,,,`, "XRPL/2.2", "XRPL/2.2"},
		{"adjacent quoted items", `"XRPL/2.2""XRPL/2.3"`, "XRPL/2.3", ""},
		{"duplicate quoted items", `"XRPL/2.2", XRPL/2.2`, "XRPL/2.2", "XRPL/2.2"},
		{"quoted suffix", `XRPL/"2.2"`, "XRPL/2.2", "XRPL/2.2"},
		{"quoted prefix", `"XRPL/"2.2`, "", ""},
		{"removed quoted version", `"XRPL/2.1"`, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.inbound, NegotiateProtocolVersion(tt.header))
			assert.Equal(t, tt.outbound, VerifyOutboundProtocolVersion(tt.header))
		})
	}
}

func TestProtocolVersionHTTPHeaderBoundary(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{"quoted", `"XRPL/2.2"`, "XRPL/2.2"},
		{"quoted pair", `"XRPL\/2.3"`, "XRPL/2.3"},
		{"non-ASCII whitespace", "\u00a0XRPL/2.2", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := "Connection: Upgrade\r\nUpgrade: " + tt.header + "\r\n\r\n"
			req, err := http.ReadRequest(bufio.NewReader(strings.NewReader("GET / HTTP/1.1\r\nHost: localhost\r\n" + headers)))
			require.NoError(t, err)
			t.Cleanup(func() { _ = req.Body.Close() })
			assert.Equal(t, tt.want, NegotiateProtocolVersion(req.Header.Get(HeaderUpgrade)))
			if tt.want == "" {
				require.ErrorIs(t, validateHandshakeRequest(req), ErrInvalidHandshake)
			} else {
				require.NoError(t, validateHandshakeRequest(req))
			}

			resp, err := http.ReadResponse(bufio.NewReader(strings.NewReader("HTTP/1.1 101 Switching Protocols\r\n"+headers)), req)
			require.NoError(t, err)
			t.Cleanup(func() { _ = resp.Body.Close() })
			assert.Equal(t, tt.want, VerifyOutboundProtocolVersion(resp.Header.Get(HeaderUpgrade)))
			if tt.want == "" {
				require.ErrorIs(t, validateHandshakeResponse(resp), ErrInvalidHandshake)
			} else {
				require.NoError(t, validateHandshakeResponse(resp))
			}
		})
	}
}
