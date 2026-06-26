package ledger

import (
	"bytes"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/keylet"
)

// TestUpdateNegativeUNL_StampsFlagLedgerSeq is the end-to-end parity check for
// rippled's VerifyPubKeyAndSeq (NegativeUNL_test.cpp): materializing a pending
// ValidatorToDisable on a flag ledger must produce a DisabledValidator entry
// whose sfFirstLedgerSequence equals that ledger's own sequence, and must clear
// the transition field (Ledger.cpp:783,792).
func TestUpdateNegativeUNL_StampsFlagLedgerSeq(t *testing.T) {
	res, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatalf("genesis.Create: %v", err)
	}
	parent := FromGenesis(res.Header, res.StateMap, res.TxMap, drops.Fees{})

	l, err := NewOpen(parent, parent.CloseTime().Add(10*time.Second))
	if err != nil {
		t.Fatalf("NewOpen: %v", err)
	}
	// Pin the open ledger to a flag-ledger boundary (seq % 256 == 0); the
	// stamped FirstLedgerSequence must equal this value.
	const flagSeq = uint32(256)
	l.header.LedgerIndex = flagSeq

	validator := make([]byte, 33)
	validator[0] = 0xED
	for i := 1; i < 33; i++ {
		validator[i] = 0x42
	}

	seed, err := pseudo.SerializeNegativeUNLSLE(&pseudo.NegativeUNLSLE{
		ValidatorToDisable: validator,
	})
	if err != nil {
		t.Fatalf("serialize seed SLE: %v", err)
	}
	key := keylet.NegativeUNL().Key
	if err := l.stateMap.Put(key, seed); err != nil {
		t.Fatalf("seed stateMap: %v", err)
	}

	if err := l.UpdateNegativeUNL(); err != nil {
		t.Fatalf("UpdateNegativeUNL: %v", err)
	}

	item, ok, err := l.stateMap.Get(key)
	if err != nil || !ok {
		t.Fatalf("read back SLE: ok=%v err=%v", ok, err)
	}
	sle, err := pseudo.ParseNegativeUNLSLE(item.Data())
	if err != nil {
		t.Fatalf("parse SLE: %v", err)
	}

	if len(sle.DisabledValidators) != 1 {
		t.Fatalf("DisabledValidators len = %d, want 1", len(sle.DisabledValidators))
	}
	dv := sle.DisabledValidators[0]
	if !bytes.Equal(dv.PublicKey, validator) {
		t.Errorf("PublicKey = %x, want %x", dv.PublicKey, validator)
	}
	if dv.FirstLedgerSequence != flagSeq {
		t.Errorf("FirstLedgerSequence = %d, want %d", dv.FirstLedgerSequence, flagSeq)
	}
	if len(sle.ValidatorToDisable) != 0 {
		t.Errorf("ValidatorToDisable not cleared: %x", sle.ValidatorToDisable)
	}
}
