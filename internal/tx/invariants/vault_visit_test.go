package invariants_test

import (
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/nft"
	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/applystate"
	"github.com/LeJamon/go-xrpl/internal/tx/invariants"
	"github.com/LeJamon/go-xrpl/internal/tx/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
)

type invariantTx struct{ tx.Transaction }

func (t invariantTx) TxAccount() string           { return t.GetCommon().Account }
func (t invariantTx) TxHasField(name string) bool { return t.GetCommon().HasField(name) }

type retypedTx struct {
	invariantTx
	txType invariants.TxType
}

func (t retypedTx) TxType() invariants.TxType { return t.txType }

// TestValidVaultSkipsVisitWithoutVault checks, on the entries real
// transactions produce, that the ValidVault result with the no-vault shortcut
// equals the result of a full per-entry visit, and that the shortcut is taken
// exactly when no Vault entry is touched.
func TestValidVaultSkipsVisitWithoutVault(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("SingleAssetVault")
	env.Close()

	gw, alice, bob := jtx.NewAccount("gateway"), jtx.NewAccount("alice"), jtx.NewAccount("bob")
	for _, account := range []*jtx.Account{gw, alice, bob} {
		env.FundAmount(account, uint64(jtx.XRP(10_000)))
	}
	env.Close()

	type submission struct {
		name         string
		transaction  tx.Transaction
		touchesVault bool
		checked      int
	}
	var current *submission
	rules := env.Rules()

	// The hook runs where the engine runs the invariant pass, on the same
	// entries and view, after every checker has passed.
	env.SetInvariantViolationHook(func(result ter.Result, table *applystate.ApplyStateTable) *invariants.InvariantViolation {
		s := current
		s.checked++
		entries := table.CollectEntries()

		fee, err := strconv.ParseUint(s.transaction.GetCommon().Fee, 10, 64)
		require.NoError(t, err, s.name)
		compare := func(name string, wrapped invariants.Transaction) *invariants.InvariantViolation {
			got, decoded := invariants.CheckValidVault(wrapped, invariants.Result(result), fee, entries, table, rules)
			if s.touchesVault {
				require.Positive(t, decoded, name)
			} else {
				require.Zero(t, decoded, name)
			}
			want := invariants.ValidVaultVisitingAll(wrapped, invariants.Result(result), fee, entries, table, rules)
			require.Equal(t, want, got, name)
			return got
		}
		wrapped := invariantTx{s.transaction}
		require.Nil(t, compare(s.name, wrapped), s.name)

		// The same entries under a type that must, or must not, modify a vault
		// exercise finalize's non-trivial outcomes on both paths.
		retype := protocol.TxTypeVaultDeposit
		if s.touchesVault {
			retype = protocol.TxTypePayment
		}
		require.NotNil(t, compare(s.name+" retyped", retypedTx{wrapped, retype}), s.name)
		return nil
	})

	submit := func(name string, transaction tx.Transaction, touchesVault bool) {
		t.Helper()
		current = &submission{name: name, transaction: transaction, touchesVault: touchesVault}
		jtx.RequireTxSuccess(t, env.Submit(transaction))
		require.Equal(t, 1, current.checked, name)
	}

	submit("payment-xrp", payment.Pay(alice, bob, uint64(jtx.XRP(1))).Build(), false)
	submit("trustset", trustset.TrustLine(bob, "USD", gw, "1000").Build(), false)
	submit("payment-iou", payment.PayIssued(gw, bob, jtx.USD(gw, 100)).Build(), false)
	submit("offer-place", offerbuild.OfferCreate(bob, jtx.XRPTxAmountFromXRP(50), jtx.USD(gw, 50)).Build(), false)
	submit("trustset-alice", trustset.TrustLine(alice, "USD", gw, "1000").Build(), false)
	submit("offer-cross", offerbuild.OfferCreate(alice, jtx.USD(gw, 50), jtx.XRPTxAmountFromXRP(50)).Build(), false)
	submit("nftoken-mint", nft.NFTokenMint(alice, 7).URI("ipfs://visit").Build(), false)

	issuanceSeq := env.Seq(gw)
	submit("mpt-issuance-create", mpt.NewMPTokenIssuanceCreate(gw.Address), false)
	mptID := keylet.MakeMPTID(issuanceSeq, gw.ID)
	mptIDHex := strings.ToUpper(hex.EncodeToString(mptID[:]))
	submit("mpt-authorize", mpt.NewMPTokenAuthorize(bob.Address, mptIDHex), false)
	submit("payment-mpt", payment.PayIssued(gw, bob, state.NewMPTAmountWithIssuanceID(25, gw.Address, mptIDHex)).Build(), false)

	vaultSeq := env.Seq(alice)
	create := vault.NewVaultCreate(alice.Address, tx.Asset{Currency: "XRP"})
	create.Common.Fee = strconv.FormatUint(env.ReserveIncrement(), 10)
	submit("vault-create", create, true)
	vaultKey := keylet.Vault(alice.ID, vaultSeq)
	vaultID := strings.ToUpper(hex.EncodeToString(vaultKey.Key[:]))
	submit("vault-deposit", vault.NewVaultDeposit(bob.Address, vaultID, tx.NewXRPAmount(1_000_000)), true)
	submit("vault-withdraw", vault.NewVaultWithdraw(bob.Address, vaultID, tx.NewXRPAmount(1_000_000)), true)
	submit("vault-delete", vault.NewVaultDelete(alice.Address, vaultID), true)
}
