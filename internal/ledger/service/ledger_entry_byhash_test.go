package service

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// getLedgerForQuery must resolve a 64-character hex ledger_hash against stored
// history rather than silently falling back to the current/validated ledger.
// Regression for ledger_entry ignoring ledger_hash and returning current state.
func TestGetLedgerForQuery_ByHash(t *testing.T) {
	svc := newOfferTestService(t)
	target := svc.validatedLedger
	if target == nil {
		t.Skip("harness has no validated ledger")
	}
	svc.putHistoryLocked(target)
	h := target.Hash()
	hhex := hex.EncodeToString(h[:])

	t.Run("resolves stored ledger by hash", func(t *testing.T) {
		l, _, err := svc.getLedgerForQuery(hhex)
		if err != nil || l == nil {
			t.Fatalf("by hash: l=%v err=%v", l, err)
		}
		if l.Hash() != h {
			t.Errorf("resolved wrong ledger: got %x want %x", l.Hash(), h)
		}
	})
	t.Run("uppercase hash also resolves", func(t *testing.T) {
		if _, _, err := svc.getLedgerForQuery(strings.ToUpper(hhex)); err != nil {
			t.Fatalf("uppercase hash: err=%v", err)
		}
	})
	t.Run("unknown hash not found", func(t *testing.T) {
		var miss [32]byte
		miss[0] = 0xAB
		_, _, err := svc.getLedgerForQuery(hex.EncodeToString(miss[:]))
		if !errors.Is(err, ErrLedgerNotFound) {
			t.Fatalf("want ErrLedgerNotFound, got %v", err)
		}
	})
	t.Run("malformed 64-char hash", func(t *testing.T) {
		_, _, err := svc.getLedgerForQuery(strings.Repeat("z", 64))
		if err == nil || err.Error() != "invalid ledger_hash" {
			t.Fatalf("want invalid ledger_hash, got %v", err)
		}
	})
}
