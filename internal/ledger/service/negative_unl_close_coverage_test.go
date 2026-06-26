package service_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	testenv "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/keylet"
)

// negUNLValidator is a deterministic ed25519-prefixed validator public key used
// to seed a pending ValidatorToDisable across the flag-ledger coverage tests.
func negUNLValidator() []byte {
	v := make([]byte, 33)
	v[0] = 0xED
	for i := 1; i < 33; i++ {
		v[i] = 0x42
	}
	return v
}

// driveStandaloneToFlagParent advances a fresh standalone service to the flag
// ledger's parent: it closes ledgers until the open ledger is seq 256 (the
// first flag ledger), with seq 255 carrying a pending ValidatorToDisable on its
// NegativeUNL SLE — the state a UNLModify pseudo-tx on the prior flag ledger
// would have left. The returned service is poised so that the next close (256)
// is the flag-ledger close under test.
func driveStandaloneToFlagParent(t *testing.T) (*service.Service, []byte) {
	t.Helper()

	svc, err := service.New(service.DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	for i := 0; svc.GetOpenLedger().Sequence() < 255; i++ {
		if i > 300 {
			t.Fatalf("drove %d ledgers without reaching open seq 255 (at %d)",
				i, svc.GetOpenLedger().Sequence())
		}
		closeLedger(t, svc)
	}
	if got := svc.GetOpenLedger().Sequence(); got != 255 {
		t.Fatalf("open seq before SLE injection = %d, want 255", got)
	}

	validator := negUNLValidator()
	seed, err := pseudo.SerializeNegativeUNLSLE(&pseudo.NegativeUNLSLE{ValidatorToDisable: validator})
	if err != nil {
		t.Fatalf("serialize NegativeUNL seed: %v", err)
	}
	if err := svc.GetOpenLedger().Insert(keylet.NegativeUNL(), seed); err != nil {
		t.Fatalf("insert NegativeUNL SLE into open 255: %v", err)
	}

	// Close 255 (carries the pending transition forward) and open 256.
	closeLedger(t, svc)
	if got := svc.GetOpenLedger().Sequence(); got != 256 {
		t.Fatalf("open seq after closing 255 = %d, want 256", got)
	}
	return svc, validator
}

// assertFlagLedgerTransitioned checks the flag ledger (the latest closed ledger,
// expected seq 256) materialised the NegativeUNL transition: ValidatorToDisable
// moved into DisabledValidators with FirstLedgerSequence stamped to the flag
// ledger, and the transition field cleared.
func assertFlagLedgerTransitioned(t *testing.T, svc *service.Service, validator []byte) {
	t.Helper()

	closed := svc.GetClosedLedger()
	if got := closed.Sequence(); got != 256 {
		t.Fatalf("closed seq = %d, want 256", got)
	}
	raw, err := closed.Read(keylet.NegativeUNL())
	if err != nil {
		t.Fatalf("read NegativeUNL SLE from flag ledger: %v", err)
	}
	if raw == nil {
		t.Fatal("NegativeUNL SLE missing from flag ledger")
	}
	sle, err := pseudo.ParseNegativeUNLSLE(raw)
	if err != nil {
		t.Fatalf("parse NegativeUNL SLE: %v", err)
	}
	if len(sle.DisabledValidators) != 1 {
		t.Fatalf("DisabledValidators len = %d, want 1 — flag-ledger transition not applied (issue #1091)",
			len(sle.DisabledValidators))
	}
	if dv := sle.DisabledValidators[0]; !bytes.Equal(dv.PublicKey, validator) {
		t.Errorf("DisabledValidators[0].PublicKey = %x, want %x", dv.PublicKey, validator)
	} else if dv.FirstLedgerSequence != 256 {
		t.Errorf("FirstLedgerSequence = %d, want 256", dv.FirstLedgerSequence)
	}
	if len(sle.ValidatorToDisable) != 0 {
		t.Errorf("ValidatorToDisable not cleared on flag ledger: %x", sle.ValidatorToDisable)
	}
}

// TestAcceptLedger_StandaloneFlagLedgerAppliesNegativeUNL covers the standalone
// AcceptLedger close path (issue #1091): with no pending transactions the close
// skips buildClosedLedgerLocked and must still apply the flag-ledger NegativeUNL
// transition via the else branch on the open ledger. Complements the consensus
// empty-tx-set regression in TestAcceptConsensusResult_FlagLedgerAppliesNegativeUNL.
func TestAcceptLedger_StandaloneFlagLedgerAppliesNegativeUNL(t *testing.T) {
	svc, validator := driveStandaloneToFlagParent(t)

	// Empty pending set: AcceptLedger takes the else branch on s.openLedger.
	closeLedger(t, svc)

	assertFlagLedgerTransitioned(t, svc, validator)
}

// TestAcceptLedger_FlagLedgerWithTxAppliesNegativeUNL covers the
// buildClosedLedgerLocked path: a flag-ledger close carrying a transaction must
// apply the NegativeUNL transition on the fresh ledger before the transaction,
// not only on the empty-tx-set close paths. A real payment makes the pending set
// non-empty so AcceptLedger rebuilds through buildClosedLedgerLocked.
func TestAcceptLedger_FlagLedgerWithTxAppliesNegativeUNL(t *testing.T) {
	svc, validator := driveStandaloneToFlagParent(t)

	env := testenv.NewTestEnv(t)
	env.SetVerifySignatures(true)
	master := testenv.MasterAccount()
	alice := testenv.NewAccount("alice")

	masterSeq := accountSeq(t, svc, master.Address)
	mustApply(t, svc, signedBlob(t, env,
		payment.Pay(master, alice, 100_000_000).Sequence(masterSeq).Build(), master))

	// Pending set is non-empty: AcceptLedger routes through buildClosedLedgerLocked.
	closeLedger(t, svc)

	assertFlagLedgerTransitioned(t, svc, validator)

	// The carried transaction must also have applied in the flag-ledger build.
	if _, err := svc.GetAccountInfo(context.Background(), alice.Address, "current"); err != nil {
		t.Errorf("alice account not created by the flag-ledger payment: %v", err)
	}
}
