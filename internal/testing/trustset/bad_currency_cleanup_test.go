package trustset

import (
	"bytes"
	"context"
	"sort"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	trustsettx "github.com/LeJamon/go-xrpl/internal/tx/trustset"
	"github.com/LeJamon/go-xrpl/keylet"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

const legacyBadCurrency = "0000000000000000000000005852500000000000"

func replaceTrustLineCurrencyWithBad(t *testing.T, data []byte) []byte {
	t.Helper()

	result := append([]byte(nil), data...)
	bad := keylet.BadCurrency()
	amounts := 0
	if err := state.WalkFields(result, func(field state.Field) error {
		if field.TypeCode == 6 && (field.FieldCode == 2 || field.FieldCode == 6 || field.FieldCode == 7) {
			if len(field.Value) != 48 {
				t.Fatalf("RippleState amount field %d has %d bytes, want 48", field.FieldCode, len(field.Value))
			}
			copy(field.Value[8:28], bad[:])
			amounts++
		}
		return nil
	}); err != nil {
		t.Fatalf("walk RippleState: %v", err)
	}
	if amounts != 3 {
		t.Fatalf("patched %d RippleState amounts, want 3", amounts)
	}
	return result
}

func replaceOwnerDirectoryIndex(t *testing.T, env *jtx.TestEnv, account [20]byte, page uint64, oldKey, newKey [32]byte) {
	t.Helper()

	dirKey := keylet.OwnerDirPage(account, page)
	data, err := env.Ledger().Read(dirKey)
	if err != nil {
		t.Fatalf("read owner directory: %v", err)
	}
	dir, err := state.ParseDirectoryNode(data)
	if err != nil {
		t.Fatalf("parse owner directory: %v", err)
	}
	found := false
	for i := range dir.Indexes {
		if dir.Indexes[i] == oldKey {
			dir.Indexes[i] = newKey
			found = true
		}
	}
	if !found {
		t.Fatal("trust line not found in owner directory")
	}
	sort.Slice(dir.Indexes, func(i, j int) bool {
		return bytes.Compare(dir.Indexes[i][:], dir.Indexes[j][:]) < 0
	})
	encoded, err := state.SerializeDirectoryNode(dir, false)
	if err != nil {
		t.Fatalf("serialize owner directory: %v", err)
	}
	if err := env.Ledger().Update(dirKey, encoded); err != nil {
		t.Fatalf("update owner directory: %v", err)
	}
}

func TestTrustSetDeletesLegacyBadCurrencyLine(t *testing.T) {
	env := jtx.NewTestEnv(t)
	issuer := jtx.NewAccount("issuer")
	holder := jtx.NewAccount("holder")
	env.Fund(issuer, holder)
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(TrustLine(holder, "USD", issuer, "100").QualityIn(123).Build()))
	env.Close()
	jtx.RequireTxSuccess(t, env.Submit(TrustLine(issuer, "USD", holder, "100").QualityIn(456).Build()))
	env.Close()

	usdKey := keylet.Line(holder.ID, issuer.ID, "USD")
	data, err := env.Ledger().Read(usdKey)
	if err != nil {
		t.Fatalf("read USD trust line: %v", err)
	}
	line, err := state.ParseRippleState(data)
	if err != nil {
		t.Fatalf("parse USD trust line: %v", err)
	}
	if line.LowQualityIn == 0 || line.HighQualityIn == 0 {
		t.Fatal("test trust line does not reserve both accounts")
	}

	badKey := keylet.Line(holder.ID, issuer.ID, legacyBadCurrency)
	if state.CompareAccountIDs(holder.ID, issuer.ID) < 0 {
		replaceOwnerDirectoryIndex(t, env, holder.ID, line.LowNode, usdKey.Key, badKey.Key)
		replaceOwnerDirectoryIndex(t, env, issuer.ID, line.HighNode, usdKey.Key, badKey.Key)
	} else {
		replaceOwnerDirectoryIndex(t, env, holder.ID, line.HighNode, usdKey.Key, badKey.Key)
		replaceOwnerDirectoryIndex(t, env, issuer.ID, line.LowNode, usdKey.Key, badKey.Key)
	}
	if err := env.Ledger().Erase(usdKey); err != nil {
		t.Fatalf("erase USD trust line: %v", err)
	}
	if err := env.Ledger().Insert(badKey, replaceTrustLineCurrencyWithBad(t, data)); err != nil {
		t.Fatalf("insert badCurrency trust line: %v", err)
	}

	accountData, err := env.Ledger().Read(keylet.Account(holder.ID))
	if err != nil {
		t.Fatalf("read holder account: %v", err)
	}
	account, err := state.ParseAccountRoot(accountData)
	if err != nil {
		t.Fatalf("parse holder account: %v", err)
	}
	holderOwnerCount := account.OwnerCount

	issuerData, err := env.Ledger().Read(keylet.Account(issuer.ID))
	if err != nil {
		t.Fatalf("read issuer account: %v", err)
	}
	issuerAccount, err := state.ParseAccountRoot(issuerData)
	if err != nil {
		t.Fatalf("parse issuer account: %v", err)
	}
	issuerOwnerCount := issuerAccount.OwnerCount
	if holderOwnerCount == 0 || issuerOwnerCount == 0 {
		t.Fatalf("test accounts are not reserved: holder=%d issuer=%d", holderOwnerCount, issuerOwnerCount)
	}

	transaction := trustsettx.NewTrustSet(
		holder.Address,
		tx.NewIssuedAmount(0, -100, legacyBadCurrency, issuer.Address),
	)
	result := transaction.Apply(&tx.ApplyContext{
		View:      env.Ledger(),
		Account:   account,
		AccountID: holder.ID,
		Config: tx.EngineConfig{
			Rules: amendment.AllSupportedRules(),
		},
		Log: xrpllog.Discard(),
		Ctx: context.Background(),
	})
	if result != ter.TesSUCCESS {
		t.Fatalf("TrustSet result = %s, want tesSUCCESS", result)
	}
	exists, err := env.Ledger().Exists(badKey)
	if err != nil {
		t.Fatalf("check badCurrency trust line: %v", err)
	}
	if exists {
		t.Fatal("legacy badCurrency trust line was not deleted")
	}
	if account.OwnerCount != holderOwnerCount {
		t.Fatalf("holder OwnerCount = %d, want %d", account.OwnerCount, holderOwnerCount)
	}

	issuerData, err = env.Ledger().Read(keylet.Account(issuer.ID))
	if err != nil {
		t.Fatalf("read updated issuer account: %v", err)
	}
	issuerAccount, err = state.ParseAccountRoot(issuerData)
	if err != nil {
		t.Fatalf("parse updated issuer account: %v", err)
	}
	if issuerAccount.OwnerCount != issuerOwnerCount {
		t.Fatalf("issuer OwnerCount = %d, want %d", issuerAccount.OwnerCount, issuerOwnerCount)
	}
}
