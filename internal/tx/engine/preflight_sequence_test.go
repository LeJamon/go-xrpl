package engine

import (
	"strings"
	"testing"

	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func TestPreflightSequenceRejectsMalformedAccountTxnID(t *testing.T) {
	sequence := uint32(1)
	e := NewEngine(nil, txcore.EngineConfig{})

	for _, accountTxnID := range []string{"00", "zz", strings.Repeat("00", 33)} {
		common := &txcore.Common{Sequence: &sequence, AccountTxnID: accountTxnID}
		if result := e.preflightSequence(common); result != ter.TemINVALID {
			t.Fatalf("preflightSequence(%q) = %s, want %s", accountTxnID, result, ter.TemINVALID)
		}
	}
}

func TestPreflightSequenceAcceptsWellFormedAccountTxnID(t *testing.T) {
	sequence := uint32(1)
	e := NewEngine(nil, txcore.EngineConfig{})
	common := &txcore.Common{
		Sequence:     &sequence,
		AccountTxnID: "0000000000000000000000000000000000000000000000000000000000000000",
	}

	if result := e.preflightSequence(common); result != ter.TesSUCCESS {
		t.Fatalf("preflightSequence() = %s, want %s", result, ter.TesSUCCESS)
	}
}
