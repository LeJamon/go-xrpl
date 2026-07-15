package service

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/protocol"
)

func TestGetLedgerForQuerySelectors(t *testing.T) {
	svc, open, closed, validated := ledgerSelectorService(t)

	tests := []struct {
		name          string
		selection     string
		want          *ledger.Ledger
		wantValidated bool
	}{
		{name: "empty defaults open", selection: "", want: open},
		{name: "current", selection: "current", want: open},
		{name: "closed", selection: "closed", want: closed},
		{name: "validated", selection: "validated", want: validated, wantValidated: true},
		{name: "numeric history", selection: strconv.FormatUint(uint64(closed.Sequence()), 10), want: closed},
		{name: "numeric open after history miss", selection: strconv.FormatUint(uint64(open.Sequence()), 10), want: open},
		{name: "leading plus numeric open", selection: "+" + strconv.FormatUint(uint64(open.Sequence()), 10), want: open},
		{name: "hash", selection: protocol.Hash256Hex(closed.Hash()), want: closed},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, gotValidated, err := getLedgerForQueryLocked(svc, test.selection)
			if err != nil {
				t.Fatalf("getLedgerForQuery(%q) returned error: %v", test.selection, err)
			}
			if got != test.want {
				t.Fatalf("getLedgerForQuery(%q) returned ledger %v, want %d", test.selection, got, test.want.Sequence())
			}
			if gotValidated != test.wantValidated {
				t.Errorf("getLedgerForQuery(%q) validated = %t, want %t", test.selection, gotValidated, test.wantValidated)
			}
			if gotValidated != got.IsValidated() {
				t.Errorf("getLedgerForQuery(%q) validated = %t, ledger reports %t", test.selection, gotValidated, got.IsValidated())
			}
		})
	}
}

func TestGetLedgerForQueryInvalidSelectors(t *testing.T) {
	svc, _, _, _ := ledgerSelectorService(t)
	tests := []struct {
		name      string
		selection string
		want      error
	}{
		{name: "malformed hash", selection: strings.Repeat("z", 64), want: ErrInvalidLedgerHash},
		{name: "malformed index", selection: "bogus", want: ErrInvalidLedgerIndex},
		{name: "sign without index", selection: "+", want: ErrInvalidLedgerIndex},
		{name: "short hash-shaped index", selection: strings.Repeat("a", 63), want: ErrInvalidLedgerIndex},
		{name: "uint32 overflow", selection: "4294967296", want: ErrInvalidLedgerIndex},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, validated, err := getLedgerForQueryLocked(svc, test.selection)
			if !errors.Is(err, test.want) {
				t.Fatalf("getLedgerForQuery(%q) error = %v, want %v", test.selection, err, test.want)
			}
			if got != nil || validated {
				t.Fatalf("getLedgerForQuery(%q) = (%v, %t), want zero result", test.selection, got, validated)
			}
		})
	}
}

func TestGetLedgerForQueryMissingTargets(t *testing.T) {
	svc := &Service{
		ledgerHistory: make(map[uint32]*ledger.Ledger),
		ledgerByHash:  make(map[[32]byte]uint32),
	}
	missingHash := [32]byte{0xff}
	tests := []struct {
		name      string
		selection string
		want      error
	}{
		{name: "empty", selection: "", want: ErrNoOpenLedger},
		{name: "current", selection: "current", want: ErrNoOpenLedger},
		{name: "closed", selection: "closed", want: ErrNoOpenLedger},
		{name: "validated", selection: "validated", want: ErrNoOpenLedger},
		{name: "sequence", selection: "99", want: ErrLedgerNotFound},
		{name: "hash", selection: protocol.Hash256Hex(missingHash), want: ErrLedgerNotFound},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, validated, err := getLedgerForQueryLocked(svc, test.selection)
			if !errors.Is(err, test.want) {
				t.Fatalf("getLedgerForQuery(%q) error = %v, want %v", test.selection, err, test.want)
			}
			if got != nil || validated {
				t.Fatalf("getLedgerForQuery(%q) = (%v, %t), want zero result", test.selection, got, validated)
			}
		})
	}
}

func TestGetLedgerForQueryUsesLedgerValidatedState(t *testing.T) {
	svc, _, closed, validated := ledgerSelectorService(t)
	svc.mu.Lock()
	svc.openLedger = validated
	svc.validatedLedger = closed
	svc.mu.Unlock()

	tests := []struct {
		selection string
		want      bool
	}{
		{selection: "current", want: true},
		{selection: "closed", want: false},
		{selection: "validated", want: false},
	}
	for _, test := range tests {
		t.Run(test.selection, func(t *testing.T) {
			l, got, err := getLedgerForQueryLocked(svc, test.selection)
			if err != nil {
				t.Fatalf("getLedgerForQuery(%q) returned error: %v", test.selection, err)
			}
			if got != test.want || got != l.IsValidated() {
				t.Fatalf("getLedgerForQuery(%q) validated = %t, ledger reports %t, want %t", test.selection, got, l.IsValidated(), test.want)
			}
		})
	}
}

func ledgerSelectorService(t *testing.T) (*Service, *ledger.Ledger, *ledger.Ledger, *ledger.Ledger) {
	t.Helper()
	svc := newOfferTestService(t)
	validated := svc.GetValidatedLedger()
	if validated == nil {
		t.Fatal("service has no validated ledger")
	}

	closed, err := ledger.NewOpen(validated, time.Unix(1_700_000_010, 0).UTC())
	if err != nil {
		t.Fatalf("create closed ledger: %v", err)
	}
	if err := closed.Close(time.Unix(1_700_000_010, 0).UTC(), 0); err != nil {
		t.Fatalf("close ledger: %v", err)
	}
	open, err := ledger.NewOpen(closed, time.Unix(1_700_000_020, 0).UTC())
	if err != nil {
		t.Fatalf("create open ledger: %v", err)
	}

	svc.mu.Lock()
	svc.validatedLedger = validated
	svc.closedLedger = closed
	svc.openLedger = open
	svc.putHistoryLocked(closed)
	svc.mu.Unlock()
	return svc, open, closed, validated
}

func getLedgerForQueryLocked(svc *Service, selection string) (*ledger.Ledger, bool, error) {
	svc.mu.RLock()
	defer svc.mu.RUnlock()
	return svc.getLedgerForQuery(selection)
}
