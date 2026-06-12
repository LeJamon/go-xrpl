package handlers

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// TestResolveLedgerSelector mirrors rippled's ledgerFromRequest precedence:
// ledger_hash wins over ledger_index when both are supplied, the hash is
// threaded through verbatim (so the service resolves the named ledger), and a
// malformed hash maps to rpcINVALID_PARAMS. With neither field set the request
// falls back to the open "current" ledger.
func TestResolveLedgerSelector(t *testing.T) {
	const validHash = "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652"

	t.Run("neither field defaults to current", func(t *testing.T) {
		sel, rpcErr := resolveLedgerSelector(types.LedgerSpecifier{})
		if rpcErr != nil {
			t.Fatalf("unexpected error: %v", rpcErr)
		}
		if sel != "current" {
			t.Errorf("selector = %q, want current", sel)
		}
	})

	t.Run("ledger_index only", func(t *testing.T) {
		sel, rpcErr := resolveLedgerSelector(types.LedgerSpecifier{LedgerIndex: "12345"})
		if rpcErr != nil {
			t.Fatalf("unexpected error: %v", rpcErr)
		}
		if sel != "12345" {
			t.Errorf("selector = %q, want 12345", sel)
		}
	})

	t.Run("ledger_hash only threads the hash", func(t *testing.T) {
		sel, rpcErr := resolveLedgerSelector(types.LedgerSpecifier{LedgerHash: validHash})
		if rpcErr != nil {
			t.Fatalf("unexpected error: %v", rpcErr)
		}
		if sel != validHash {
			t.Errorf("selector = %q, want the hash itself", sel)
		}
		// A hash selector must resolve to closed-ledger shape, never the open one.
		if isOpenLedgerSelector(sel) {
			t.Errorf("hash selector must not be treated as the open ledger")
		}
	})

	t.Run("hash wins when both supplied", func(t *testing.T) {
		sel, rpcErr := resolveLedgerSelector(types.LedgerSpecifier{
			LedgerHash:  validHash,
			LedgerIndex: "validated",
		})
		if rpcErr != nil {
			t.Fatalf("unexpected error: %v", rpcErr)
		}
		if sel != validHash {
			t.Errorf("selector = %q, want the hash (precedence over ledger_index)", sel)
		}
	})

	t.Run("malformed hash — wrong length", func(t *testing.T) {
		_, rpcErr := resolveLedgerSelector(types.LedgerSpecifier{LedgerHash: "DEADBEEF"})
		if rpcErr == nil {
			t.Fatalf("want rpcINVALID_PARAMS, got nil")
		}
		if rpcErr.Code != types.RpcErrorInvalidParams("").Code {
			t.Errorf("error code = %d, want invalid_params", rpcErr.Code)
		}
	})

	t.Run("malformed hash — non-hex", func(t *testing.T) {
		_, rpcErr := resolveLedgerSelector(types.LedgerSpecifier{LedgerHash: strings.Repeat("z", 64)})
		if rpcErr == nil {
			t.Fatalf("want rpcINVALID_PARAMS for non-hex hash, got nil")
		}
	})
}
