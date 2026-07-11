package service

import (
	"encoding/binary"
	"testing"
	"time"
)

func rangeTestHash(seq uint32) [32]byte {
	var hash [32]byte
	hash[0] = 0xA5
	binary.BigEndian.PutUint32(hash[28:], seq)
	return hash
}

func newRangeTestService(t *testing.T, lo, hi, validated, closed, fetchDepth uint32) *Service {
	t.Helper()
	cfg := DefaultConfig()
	cfg.FetchDepth = fetchDepth
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var parent [32]byte
	for seq := lo; seq <= hi; seq++ {
		hash := rangeTestHash(seq)
		svc.putHistoryLocked(makeStubLedger(t, seq, hash, parent))
		parent = hash
	}
	if validated != 0 {
		svc.validatedLedger = svc.ledgerHistory[validated]
		if svc.validatedLedger == nil {
			t.Fatalf("validated seq %d not in history [%d,%d]", validated, lo, hi)
		}
	}
	if closed != 0 {
		svc.closedLedger = svc.ledgerHistory[closed]
		if svc.closedLedger == nil {
			t.Fatalf("closed seq %d not in history [%d,%d]", closed, lo, hi)
		}
	}
	return svc
}

func requireAdvertisableRange(t *testing.T, svc *Service, wantFirst, wantLast uint32, wantOK bool) {
	t.Helper()
	first, last, ok := svc.AdvertisableLedgerRange()
	if first != wantFirst || last != wantLast || ok != wantOK {
		t.Fatalf("AdvertisableLedgerRange = (%d, %d, %v), want (%d, %d, %v)",
			first, last, ok, wantFirst, wantLast, wantOK)
	}
}

func TestAdvertisableLedgerRange_NoValidated(t *testing.T) {
	svc := newRangeTestService(t, 10, 100, 0, 100, 0)
	requireAdvertisableRange(t, svc, 0, 0, false)
}

func TestAdvertisableLedgerRange_EndsAtValidatedTip(t *testing.T) {
	svc := newRangeTestService(t, 10, 100, 80, 100, 0)
	requireAdvertisableRange(t, svc, 10, 80, true)
}

func TestAdvertisableLedgerRange_UsesContiguousSuffix(t *testing.T) {
	t.Run("gap", func(t *testing.T) {
		svc := newRangeTestService(t, 10, 100, 100, 100, 0)
		svc.deleteHistoryLocked(60)
		requireAdvertisableRange(t, svc, 61, 100, true)
	})

	t.Run("parent mismatch", func(t *testing.T) {
		svc := newRangeTestService(t, 10, 100, 100, 100, 0)
		hash := svc.ledgerHistory[80].Hash()
		svc.putHistoryLocked(makeStubLedger(t, 80, hash, [32]byte{0xFF}))
		requireAdvertisableRange(t, svc, 80, 100, true)
	})
}

func TestAdvertisableLedgerRange_RequiresValidatedTipInHistory(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		svc := newRangeTestService(t, 10, 100, 100, 100, 0)
		delete(svc.ledgerHistory, 100)
		requireAdvertisableRange(t, svc, 0, 0, false)
	})

	t.Run("different hash", func(t *testing.T) {
		svc := newRangeTestService(t, 10, 100, 100, 100, 0)
		svc.putHistoryLocked(makeStubLedger(t, 100, [32]byte{0xFF}, rangeTestHash(99)))
		requireAdvertisableRange(t, svc, 0, 0, false)
	})
}

func TestAdvertisableLedgerRange_ClampsToConfiguredAndOnlineFloors(t *testing.T) {
	svc := newRangeTestService(t, 10, 100, 90, 100, 30)
	requireAdvertisableRange(t, svc, 70, 90, true)

	svc.SetMinimumOnlineFunc(func() uint32 { return 60 })
	requireAdvertisableRange(t, svc, 70, 90, true)

	svc.SetMinimumOnlineFunc(func() uint32 { return 80 })
	requireAdvertisableRange(t, svc, 80, 90, true)
}

func TestAdvertisableLedgerRange_FloorAboveTip(t *testing.T) {
	svc := newRangeTestService(t, 10, 100, 100, 100, 0)
	svc.SetMinimumOnlineFunc(func() uint32 { return 200 })
	requireAdvertisableRange(t, svc, 0, 0, false)
}

func TestAdvertisableLedgerRange_MinimumOnlineCallbackRunsUnlocked(t *testing.T) {
	svc := newRangeTestService(t, 10, 100, 100, 100, 0)
	svc.SetMinimumOnlineFunc(func() uint32 {
		done := make(chan struct{})
		go func() {
			svc.SetMinimumOnlineFunc(nil)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("minimum-online callback ran while Service.mu was held")
		}
		return 0
	})
	requireAdvertisableRange(t, svc, 10, 100, true)
}
