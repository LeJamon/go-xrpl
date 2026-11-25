package did_test

import (
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/did"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
)

func TestEnabled(t *testing.T) {
	runWithFeatureSets(t, testEnabled)
}

func testEnabled(t *testing.T, fixEmptyDID bool) {
	env := setupEnv(t, fixEmptyDID)

	env.DisableFeature("DID")

	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	tx1 := did.DIDSet(alice).URI("uri").Document("doc").Data("data").Build()
	result := env.Submit(tx1)
	jtx.RequireTxFail(t, result, jtx.TemDISABLED)
	env.Close()

	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0 after failed DIDSet, got %d", env.OwnerCount(alice))
	}

	tx2 := did.DIDDelete(alice).Build()
	result = env.Submit(tx2)
	jtx.RequireTxFail(t, result, jtx.TemDISABLED)
	env.Close()
}

func TestEmptyDIDWithoutReserve(t *testing.T) {
	type outcome struct {
		Metadata        string `json:"metadata"`
		TransactionRoot string `json:"transaction_root"`
	}
	var fixture struct {
		LedgerIndex      uint32  `json:"ledger_index"`
		NetworkID        uint32  `json:"network_id"`
		ReserveBase      uint64  `json:"reserve_base"`
		ReserveIncrement uint64  `json:"reserve_increment"`
		Fee              uint64  `json:"fee"`
		ParentAccount    string  `json:"parent_account"`
		Transaction      string  `json:"transaction"`
		WithFix          outcome `json:"with_fix"`
		WithoutFix       outcome `json:"without_fix"`
	}
	data, err := os.ReadFile("testdata/empty_did_without_reserve.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &fixture))
	blob, err := hex.DecodeString(fixture.Transaction)
	require.NoError(t, err)
	parentAccount, err := hex.DecodeString(fixture.ParentAccount)
	require.NoError(t, err)

	runWithFeatureSets(t, func(t *testing.T, fixEmptyDID bool) {
		env := setupEnv(t, fixEmptyDID)
		env.SetNetworkID(fixture.NetworkID)
		env.SetReserves(fixture.ReserveBase, fixture.ReserveIncrement)
		env.SetVerifySignatures(true)
		for env.LedgerSeq() < fixture.LedgerIndex {
			env.Close()
		}
		env.SetOpenLedger(false)

		txn, err := tx.ParseFromBinary(blob)
		require.NoError(t, err)
		owner := jtx.NewAccountWithAddress("owner", txn.GetCommon().Account)
		require.NoError(t, env.Ledger().Insert(keylet.Account(owner.ID), parentAccount))
		balance := env.Balance(owner)
		require.Less(t, balance-fixture.Fee, env.ReserveBase()+env.ReserveIncrement())
		result := env.SubmitWithOptions(txn, jtx.SubmitOptions{SkipSignature: true})
		wantCode, want := jtx.TecINSUFFICIENT_RESERVE, fixture.WithoutFix
		if fixEmptyDID {
			wantCode, want = "tecEMPTY_DID", fixture.WithFix
		}
		jtx.RequireTxClaimed(t, result, wantCode)
		require.Equal(t, fixture.Fee, result.Fee)
		require.Equal(t, balance-fixture.Fee, env.Balance(owner))
		requireDIDAbsent(t, env, owner)

		meta, err := tx.SerializeMetadata(result.Metadata)
		require.NoError(t, err)
		wantMeta, err := hex.DecodeString(want.Metadata)
		require.NoError(t, err)
		require.Equal(t, wantMeta, meta)
		env.Close()
		wantRoot, err := protocol.Hash256FromHex(want.TransactionRoot)
		require.NoError(t, err)
		require.Equal(t, wantRoot, env.LastClosedLedger().Header().TxHash)
	})
}

func TestDIDCreateAtMaxOwnerCount(t *testing.T) {
	for _, tc := range []struct {
		name            string
		sponsorEnabled  bool
		sponsoredOwners uint32
		postFeeBalance  uint64
		wantCreated     bool
	}{
		{name: "legacy reserve", postFeeBalance: math.MaxUint32 - 1, wantCreated: true},
		{name: "one drop short", sponsorEnabled: true, postFeeBalance: math.MaxUint32 - 1},
		{name: "exact reserve", sponsorEnabled: true, postFeeBalance: math.MaxUint32, wantCreated: true},
		{name: "one sponsored owner", sponsorEnabled: true, sponsoredOwners: 1, postFeeBalance: math.MaxUint32 - 1},
		{name: "two sponsored owners", sponsorEnabled: true, sponsoredOwners: 2, postFeeBalance: math.MaxUint32 - 1, wantCreated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			if tc.sponsorEnabled {
				env.EnableFeature("Sponsor")
			} else {
				env.DisableFeature("Sponsor")
			}
			env.SetReserves(0, 1)
			owner := jtx.NewAccount("owner")
			env.FundAmount(owner, tc.postFeeBalance+env.BaseFee())
			env.Close()
			env.SetOpenLedger(false)
			account, err := state.ReadAccountRoot(env.Ledger(), owner.ID)
			require.NoError(t, err)
			require.NotNil(t, account)
			account.OwnerCount = math.MaxUint32
			account.SponsoredOwnerCount = tc.sponsoredOwners
			entry, err := state.SerializeAccountRoot(account)
			require.NoError(t, err)
			require.NoError(t, env.Ledger().Update(keylet.Account(owner.ID), entry))

			result := env.Submit(did.DIDSet(owner).URI("uri").Build())
			if tc.wantCreated {
				jtx.RequireTxSuccess(t, result)
			} else {
				jtx.RequireTxClaimed(t, result, jtx.TecINSUFFICIENT_RESERVE)
			}
			require.Equal(t, tc.wantCreated, env.LedgerEntryExists(keylet.DID(owner.ID)))
			require.Equal(t, uint32(math.MaxUint32), env.OwnerCount(owner))
			require.Equal(t, tc.postFeeBalance, env.Balance(owner))
		})
	}
}

func TestAccountReserve(t *testing.T) {
	runWithFeatureSets(t, testAccountReserve)
}

func testAccountReserve(t *testing.T, fixEmptyDID bool) {
	env := setupEnv(t, fixEmptyDID)

	alice := jtx.NewAccount("alice")

	acctReserve := env.ReserveBase()
	incReserve := env.ReserveIncrement()
	baseFee := env.BaseFee()

	env.FundAmount(alice, acctReserve)
	env.Close()

	balance := env.Balance(alice)
	require.Equal(t, acctReserve, balance)
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	tx1 := did.DIDSet(alice).URI("uri").Document("doc").Data("data").Build()
	result := env.Submit(tx1)
	jtx.RequireTxClaimed(t, result, jtx.TecINSUFFICIENT_RESERVE)
	env.Close()
	requireDIDAbsent(t, env, alice)
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	master := env.MasterAccount()
	payTx := payment.Pay(master, alice, incReserve+2*baseFee-1).Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx))
	env.Close()
	require.Equal(t, acctReserve+incReserve+baseFee-1, env.Balance(alice))

	tx2 := did.DIDSet(alice).URI("uri").Document("doc").Data("data").Build()
	result = env.Submit(tx2)
	jtx.RequireTxClaimed(t, result, jtx.TecINSUFFICIENT_RESERVE)
	env.Close()
	requireDIDAbsent(t, env, alice)
	require.Equal(t, acctReserve+incReserve-1, env.Balance(alice))
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	payTx2 := payment.Pay(master, alice, baseFee+1).Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx2))
	env.Close()
	require.Equal(t, acctReserve+incReserve+baseFee, env.Balance(alice))

	tx3 := did.DIDSet(alice).URI("uri").Document("doc").Data("data").Build()
	result = env.Submit(tx3)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	require.Equal(t, acctReserve+incReserve, env.Balance(alice))
	if env.OwnerCount(alice) != 1 {
		t.Errorf("Expected owner count 1, got %d", env.OwnerCount(alice))
	}

	tx4 := did.DIDDelete(alice).Build()
	result = env.Submit(tx4)
	jtx.RequireTxSuccess(t, result)
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0 after delete, got %d", env.OwnerCount(alice))
	}
	env.Close()
}
