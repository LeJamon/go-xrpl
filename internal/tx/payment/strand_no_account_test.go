package payment

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func TestToStrandsRejectsNoAccount(t *testing.T) {
	normalID := [24]byte{23: 2}
	badID := [24]byte{23: 1}
	normalAmount := newMPTAmount(100, normalID)
	badAmount := newMPTAmount(100, badID)
	normalSource := [20]byte{19: 3}
	normalDestination := [20]byte{19: 4}

	tests := []struct {
		name    string
		source  [20]byte
		dest    [20]byte
		deliver tx.Amount
		sendMax *tx.Amount
	}{
		{
			name:    "source account",
			source:  noAccountID,
			dest:    normalDestination,
			deliver: normalAmount,
			sendMax: &normalAmount,
		},
		{
			name:    "destination account",
			source:  normalSource,
			dest:    noAccountID,
			deliver: normalAmount,
			sendMax: &normalAmount,
		},
		{
			name:    "SendMax issuer",
			source:  normalSource,
			dest:    normalDestination,
			deliver: normalAmount,
			sendMax: &badAmount,
		},
		{
			name:    "deliver issuer",
			source:  normalSource,
			dest:    normalDestination,
			deliver: badAmount,
			sendMax: &normalAmount,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, result := ToStrands(
				nil,
				test.source,
				test.dest,
				test.deliver,
				test.sendMax,
				nil,
				true,
				false,
			)
			if result != ter.TemBAD_PATH {
				t.Fatalf("ToStrands() = %v, want TemBAD_PATH", result)
			}
		})
	}
}

func TestPathElementsRejectNoAccount(t *testing.T) {
	address := state.EncodeAccountIDSafe(noAccountID)
	for _, path := range [][]PathStep{
		{{Account: address}},
		{{Currency: "USD", Issuer: address}},
	} {
		if result := validatePathElementShapes(path); result != ter.TemBAD_PATH {
			t.Fatalf("validatePathElementShapes(%+v) = %v, want TemBAD_PATH", path, result)
		}
	}
}

func TestToStrandWithLoopCheckRejectsNoAccount(t *testing.T) {
	normalSource := [20]byte{19: 3}
	normalDestination := [20]byte{19: 4}
	normalIssue := Issue{IsMPT: true, MPTID: [24]byte{23: 2}, Issuer: normalSource}
	badIssue := Issue{IsMPT: true, MPTID: [24]byte{23: 1}, Issuer: noAccountID}

	tests := []struct {
		name    string
		source  [20]byte
		dest    [20]byte
		deliver Issue
		sendMax *Issue
	}{
		{name: "source account", source: noAccountID, dest: normalDestination, deliver: normalIssue},
		{name: "destination account", source: normalSource, dest: noAccountID, deliver: normalIssue},
		{name: "deliver issuer", source: normalSource, dest: normalDestination, deliver: badIssue},
		{name: "SendMax issuer", source: normalSource, dest: normalDestination, deliver: normalIssue, sendMax: &badIssue},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, result := ToStrandWithLoopCheck(
				nil,
				test.source,
				test.dest,
				test.deliver,
				test.sendMax,
				nil,
				true,
				false,
			)
			if result != ter.TemBAD_PATH {
				t.Fatalf("ToStrandWithLoopCheck() = %v, want TemBAD_PATH", result)
			}
		})
	}
}
