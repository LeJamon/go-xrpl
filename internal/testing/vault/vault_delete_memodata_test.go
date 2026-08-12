package vault_test

import (
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
)

func TestVaultDeleteMemoData(t *testing.T) {
	env := newVaultEnv(t)
	owner := jtx.NewAccount("owner")
	env.Fund(owner)

	missingID := strings.Repeat("01", 32)
	maximumData := strings.Repeat("AA", vault.MaxVaultDataLength)

	sequenceBefore := env.Seq(owner)
	balanceBefore := env.Balance(owner)
	disabled := vault.NewVaultDelete(owner.Address, missingID)
	disabled.MemoData = maximumData
	disabledResult := env.Submit(disabled)
	jtx.RequireTxFail(t, disabledResult, jtx.TemDISABLED)
	if disabledResult.Applied || disabledResult.Metadata != nil || disabledResult.Fee != 0 {
		t.Fatalf("temDISABLED result committed effects: %+v", disabledResult)
	}
	if got := env.Seq(owner); got != sequenceBefore {
		t.Fatalf("sequence after temDISABLED = %d, want %d", got, sequenceBefore)
	}
	if got := env.Balance(owner); got != balanceBefore {
		t.Fatalf("balance after temDISABLED = %d, want %d", got, balanceBefore)
	}

	env.EnableFeature("LendingProtocolV1_1")
	env.Close()

	empty := vault.NewVaultDelete(owner.Address, missingID)
	empty.Common.SetPresentFields(map[string]bool{"MemoData": true})
	emptyResult := env.Submit(empty)
	jtx.RequireTxFail(t, emptyResult, jtx.TemMALFORMED)
	if emptyResult.Applied || emptyResult.Metadata != nil || emptyResult.Fee != 0 {
		t.Fatalf("empty MemoData committed effects: %+v", emptyResult)
	}
	if got := env.Seq(owner); got != sequenceBefore {
		t.Fatalf("sequence after empty MemoData = %d, want %d", got, sequenceBefore)
	}

	noEntry := vault.NewVaultDelete(owner.Address, missingID)
	noEntry.MemoData = maximumData
	noEntryResult := env.Submit(noEntry)
	jtx.RequireTxClaimed(t, noEntryResult, jtx.TecNO_ENTRY)
	if !noEntryResult.Applied || noEntryResult.Metadata == nil || noEntryResult.Fee == 0 {
		t.Fatalf("tecNO_ENTRY did not claim normal fee/sequence effects: %+v", noEntryResult)
	}
	if got := env.Seq(owner); got != sequenceBefore+1 {
		t.Fatalf("sequence after tecNO_ENTRY = %d, want %d", got, sequenceBefore+1)
	}

	createSequence := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
	create.Common.Fee = createFee
	jtx.RequireTxSuccess(t, env.Submit(create))
	id := vaultID(owner, createSequence)

	deleteTx := vault.NewVaultDelete(owner.Address, id)
	deleteTx.MemoData = maximumData
	deleteResult := env.Submit(deleteTx)
	jtx.RequireTxSuccess(t, deleteResult)
	if deleteResult.Metadata == nil {
		t.Fatal("successful VaultDelete with MemoData returned no metadata")
	}
	if env.VaultExists(id) {
		t.Fatal("vault still exists after VaultDelete with MemoData")
	}
}
