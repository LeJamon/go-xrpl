package ledger

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/keylet"
)

func ledgerWithAmendments(t *testing.T, enabled [][32]byte) *Ledger {
	t.Helper()

	result, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatalf("create genesis: %v", err)
	}
	parent := FromGenesis(result.Header, result.StateMap, result.TxMap, drops.Fees{})
	l, err := NewOpen(parent, parent.CloseTime().Add(10*time.Second))
	if err != nil {
		t.Fatalf("create open ledger: %v", err)
	}
	data, err := pseudo.SerializeAmendmentsSLE(&pseudo.AmendmentsSLE{Amendments: enabled})
	if err != nil {
		t.Fatalf("serialize amendments: %v", err)
	}
	if err := l.Update(keylet.Amendments(), data); err != nil {
		t.Fatalf("update amendments: %v", err)
	}
	if err := l.Close(l.CloseTime(), 0); err != nil {
		t.Fatalf("close ledger: %v", err)
	}
	return l
}

func TestLedgerRulesReflectAmendmentState(t *testing.T) {
	negativeUNL := amendment.FeatureByName("NegativeUNL")
	if negativeUNL == nil {
		t.Fatal("NegativeUNL not registered")
	}
	priceOracle := amendment.FeatureByName("PriceOracle")
	if priceOracle == nil {
		t.Fatal("PriceOracle not registered")
	}

	rules := ledgerWithAmendments(t, [][32]byte{priceOracle.ID}).Rules()
	if !rules.Enabled(negativeUNL.ID) {
		t.Error("retired NegativeUNL amendment is disabled")
	}
	if !rules.Enabled(priceOracle.ID) {
		t.Error("listed PriceOracle amendment is disabled")
	}

	rules = ledgerWithAmendments(t, [][32]byte{negativeUNL.ID}).Rules()
	if rules.Enabled(priceOracle.ID) {
		t.Error("absent PriceOracle amendment is enabled")
	}
}

func TestOpenLedgerRulesStayFixedUntilClose(t *testing.T) {
	priceOracle := amendment.FeatureByName("PriceOracle")
	if priceOracle == nil {
		t.Fatal("PriceOracle not registered")
	}
	parent := ledgerWithAmendments(t, [][32]byte{priceOracle.ID})
	l, err := NewOpen(parent, parent.CloseTime().Add(10*time.Second))
	if err != nil {
		t.Fatalf("create child ledger: %v", err)
	}

	did := amendment.FeatureByName("DID")
	if did == nil {
		t.Fatal("DID not registered")
	}
	data, err := pseudo.SerializeAmendmentsSLE(&pseudo.AmendmentsSLE{Amendments: [][32]byte{did.ID}})
	if err != nil {
		t.Fatalf("serialize amendments: %v", err)
	}
	if err := l.Update(keylet.Amendments(), data); err != nil {
		t.Fatalf("update amendments: %v", err)
	}
	if !l.Rules().Enabled(priceOracle.ID) || l.Rules().Enabled(did.ID) {
		t.Fatal("open ledger did not retain its parent rules")
	}
	if err := l.Close(l.CloseTime(), 0); err != nil {
		t.Fatalf("close child ledger: %v", err)
	}
	if l.Rules().Enabled(priceOracle.ID) || !l.Rules().Enabled(did.ID) {
		t.Error("closed ledger did not adopt its final amendment rules")
	}
}

func TestCloseRejectsMalformedAmendmentsEntry(t *testing.T) {
	result, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatalf("create genesis: %v", err)
	}
	parent := FromGenesis(result.Header, result.StateMap, result.TxMap, drops.Fees{})
	l, err := NewOpen(parent, parent.CloseTime().Add(10*time.Second))
	if err != nil {
		t.Fatalf("create open ledger: %v", err)
	}
	if err := l.Update(keylet.Amendments(), bytes.Repeat([]byte{0xff}, 16)); err != nil {
		t.Fatalf("update amendments: %v", err)
	}
	if err := l.Close(l.CloseTime(), 0); err == nil {
		t.Fatal("close accepted malformed Amendments entry")
	}
	if l.State() != StateOpen {
		t.Fatalf("failed close changed state to %v", l.State())
	}
}

func TestHeaderConstructorsRejectMalformedAmendments(t *testing.T) {
	result, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatalf("create genesis: %v", err)
	}
	stateMap, err := result.StateMap.SnapshotMutable()
	if err != nil {
		t.Fatalf("snapshot state map: %v", err)
	}
	if err := stateMap.Put(keylet.Amendments().Key, bytes.Repeat([]byte{0xff}, 16)); err != nil {
		t.Fatalf("replace amendments: %v", err)
	}
	if err := stateMap.SetImmutable(); err != nil {
		t.Fatalf("freeze state map: %v", err)
	}
	parent, err := NewFromHeader(result.Header, stateMap, result.TxMap, drops.Fees{})
	if err == nil {
		t.Fatal("NewFromHeader accepted malformed Amendments entry")
	}
	if parent != nil {
		t.Fatal("NewFromHeader returned a ledger for malformed Amendments state")
	}
	if _, err := NewOpenWithHeader(result.Header, stateMap, result.TxMap, drops.Fees{}); err == nil {
		t.Fatal("NewOpenWithHeader accepted malformed Amendments entry")
	}
}

func TestNewFromHeaderOwnsImmutableState(t *testing.T) {
	result, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatalf("create genesis: %v", err)
	}
	stateMap, err := result.StateMap.SnapshotMutable()
	if err != nil {
		t.Fatalf("snapshot state map: %v", err)
	}
	l, err := NewFromHeader(result.Header, stateMap, result.TxMap, drops.Fees{})
	if err != nil {
		t.Fatalf("NewFromHeader: %v", err)
	}

	custom := [32]byte{0xfa}
	data, err := pseudo.SerializeAmendmentsSLE(&pseudo.AmendmentsSLE{Amendments: [][32]byte{custom}})
	if err != nil {
		t.Fatalf("serialize amendments: %v", err)
	}
	if err := stateMap.Put(keylet.Amendments().Key, data); err != nil {
		t.Fatalf("mutate source state map: %v", err)
	}
	if l.Rules().Enabled(custom) {
		t.Fatal("source-map mutation changed constructed ledger rules")
	}
	ledgerData, err := l.Read(keylet.Amendments())
	if err != nil {
		t.Fatalf("read constructed ledger: %v", err)
	}
	if bytes.Equal(ledgerData, data) {
		t.Fatal("source-map mutation changed constructed ledger state")
	}
}

func TestAdoptStateRejectsImmutableLedger(t *testing.T) {
	priceOracle := amendment.FeatureByName("PriceOracle")
	if priceOracle == nil {
		t.Fatal("PriceOracle not registered")
	}
	target := ledgerWithAmendments(t, [][32]byte{priceOracle.ID})
	source, err := target.MutableSnapshot()
	if err != nil {
		t.Fatalf("create source snapshot: %v", err)
	}
	if err := target.AdoptState(source); !errors.Is(err, ErrLedgerImmutable) {
		t.Fatalf("AdoptState error = %v, want ErrLedgerImmutable", err)
	}
}
