package oracle_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	oracletest "github.com/LeJamon/go-xrpl/internal/testing/oracle"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
)

func TestOracleSet_ReplayUsesApplicationViewCloseTime(t *testing.T) {
	env := jtx.NewTestEnv(t)
	owner := jtx.NewAccount("owner")
	env.Fund(owner)

	for env.Ledger().CloseTimeResolution() != 10 {
		env.Close()
	}
	const parentCloseTime = uint32(835360951)
	env.CloseToParentCloseTime(parentCloseTime)

	env.EnableOpenLedgerReplay()
	lastUpdateTime := uint32(protocol.RippleEpochUnix) + parentCloseTime + 301
	result := env.Submit(oracletest.OracleSet(owner, 1, lastUpdateTime).
		ProviderHex(32).
		AssetClassHex(8).
		AddPrice("XRP", "USD", 740, 1).
		Fee(env.BaseFee()).
		Build())
	jtx.RequireTxClaimed(t, result, "tecINVALID_UPDATE_TIME")

	env.SetTime(protocol.FromRippleTime(835360942))
	env.Close()

	closed := env.LastClosedLedger()
	if got := protocol.ToRippleTime(closed.CloseTime()); got != 835360952 {
		t.Fatalf("stored close time: got %d want 835360952", got)
	}
	if !env.LedgerEntryExists(keylet.Oracle(owner.ID, 1)) {
		t.Fatal("OracleSet was not accepted against application view close time 835360961")
	}
}
