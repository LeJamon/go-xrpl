package lending_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

const (
	maxXRPDrops      = "100000000000000000"
	maxXRPDropsPlus1 = "100000000000000001"
)

type valueRangeXRPVault struct {
	env      *jtx.TestEnv
	owner    *jtx.Account
	borrower *jtx.Account
	vaultID  string
	vault    keylet.Keylet
}

type valueRangeBroker struct {
	env       *jtx.TestEnv
	lender    *jtx.Account
	borrower  *jtx.Account
	brokerID  string
	vaultID   string
	vault     keylet.Keylet
	broker    keylet.Keylet
	loan      keylet.Keylet
	holding   keylet.Keylet
	assetKind string
}

type valueRangeEntry struct {
	key keylet.Keylet
	raw []byte
}

type valueRangeAccount struct {
	balance    uint64
	sequence   uint32
	ownerCount uint32
}

func newValueRangeXRPVault(t *testing.T) *valueRangeXRPVault {
	t.Helper()
	env := newLendingEnv(t)
	owner := jtx.NewAccount("range-owner")
	borrower := jtx.NewAccount("range-borrower")
	env.FundAmount(owner, 10_000_000_000)
	env.FundAmount(borrower, 10_000_000_000)
	vaultSequence := env.Seq(owner)
	vaultID := setupXRPVault(t, env, owner, 1_000)
	env.Close()
	return &valueRangeXRPVault{
		env:      env,
		owner:    owner,
		borrower: borrower,
		vaultID:  vaultID,
		vault:    keylet.Vault(owner.AccountID(), vaultSequence),
	}
}

func newValueRangeBroker(t *testing.T, kind string) *valueRangeBroker {
	t.Helper()
	if kind != "XRP" {
		f := newLoanSetAssetFixture(t, kind)
		f.env.Close()
		return &valueRangeBroker{
			env:       f.env,
			lender:    f.owner,
			borrower:  f.borrower,
			brokerID:  f.brokerID,
			vaultID:   f.vaultID,
			vault:     f.vaultKey,
			broker:    keylet.LoanBrokerByID(f.brokerKey),
			loan:      keylet.Loan(f.brokerKey, 1),
			holding:   f.holdingKey(f.borrower),
			assetKind: kind,
		}
	}

	base := newValueRangeXRPVault(t)
	brokerSequence := base.env.Seq(base.owner)
	jtx.RequireTxSuccess(t, base.env.Submit(lending.NewLoanBrokerSet(base.owner.Address, base.vaultID)))
	base.env.Close()
	brokerID := brokerID(base.owner, brokerSequence)
	brokerKey := keylet.LoanBroker(base.owner.AccountID(), brokerSequence)
	return &valueRangeBroker{
		env:       base.env,
		lender:    base.owner,
		borrower:  base.borrower,
		brokerID:  brokerID,
		vaultID:   base.vaultID,
		vault:     base.vault,
		broker:    brokerKey,
		loan:      keylet.Loan(brokerKey.Key, 1),
		assetKind: kind,
	}
}

func (f *valueRangeBroker) relevantEntries() []keylet.Keylet {
	keys := []keylet.Keylet{f.vault, f.broker, f.loan}
	if f.assetKind != "XRP" {
		keys = append(keys, f.holding)
	}
	return keys
}

func signedValueRangeLoan(t *testing.T, f *valueRangeBroker, principal, field string) *lending.LoanSet {
	t.Helper()
	loanSet := lending.NewLoanSet(f.borrower.Address, f.brokerID, principal)
	loanSet.Counterparty = f.lender.Address
	if field != "PrincipalRequested" {
		value := maxXRPDropsPlus1
		switch field {
		case "LoanOriginationFee":
			loanSet.LoanOriginationFee = &value
		case "LoanServiceFee":
			loanSet.LoanServiceFee = &value
		case "LatePaymentFee":
			loanSet.LatePaymentFee = &value
		case "ClosePaymentFee":
			loanSet.ClosePaymentFee = &value
		default:
			t.Fatalf("unknown LoanSet value field %q", field)
		}
	}
	loanSet.GetCommon().Fee = "20"
	loanSet.GetCommon().SigningPubKey = f.borrower.PublicKeyHex()
	signature, err := txsign.SignCounterpartyWithRules(
		loanSet,
		f.lender.PublicKeyHex(),
		"00"+f.lender.PrivateKeyHex(),
		f.env.Rules(),
	)
	require.NoError(t, err)
	loanSet.GetCommon().CounterpartySignature = signature
	return loanSet
}

func snapshotValueRangeEntries(t *testing.T, env *jtx.TestEnv, keys ...keylet.Keylet) []valueRangeEntry {
	t.Helper()
	snapshot := make([]valueRangeEntry, 0, len(keys))
	for _, key := range keys {
		raw, err := env.LedgerEntry(key)
		require.NoError(t, err)
		snapshot = append(snapshot, valueRangeEntry{key: key, raw: append([]byte(nil), raw...)})
	}
	return snapshot
}

func requireValueRangeEntriesUnchanged(t *testing.T, env *jtx.TestEnv, snapshot []valueRangeEntry) {
	t.Helper()
	for _, before := range snapshot {
		after, err := env.LedgerEntry(before.key)
		require.NoError(t, err)
		require.Equal(t, before.raw, after, "ledger entry %v changed", before.key.Type)
	}
}

func snapshotValueRangeAccount(env *jtx.TestEnv, account *jtx.Account) valueRangeAccount {
	return valueRangeAccount{
		balance:    env.Balance(account),
		sequence:   env.Seq(account),
		ownerCount: env.OwnerCount(account),
	}
}

func requireValueRangeException(t *testing.T, result jtx.TxResult, env *jtx.TestEnv, payer *jtx.Account, state valueRangeAccount, entries []valueRangeEntry) {
	t.Helper()
	require.Equal(t, ter.TefEXCEPTION.String(), result.Code)
	require.False(t, result.Applied)
	require.Zero(t, result.Fee)
	require.Nil(t, result.Metadata)
	require.Equal(t, state, snapshotValueRangeAccount(env, payer))
	requireValueRangeEntriesUnchanged(t, env, entries)

	env.Close()
	require.Equal(t, state, snapshotValueRangeAccount(env, payer))
	requireValueRangeEntriesUnchanged(t, env, entries)
}

func TestLoanBrokerSetNativeValueRange(t *testing.T) {
	t.Run("create rejects above native maximum and accepts maximum", func(t *testing.T) {
		base := newValueRangeXRPVault(t)
		brokerSequence := base.env.Seq(base.owner)
		brokerKey := keylet.LoanBroker(base.owner.AccountID(), brokerSequence)
		beforeState := snapshotValueRangeAccount(base.env, base.owner)
		beforeEntries := snapshotValueRangeEntries(t, base.env, base.vault, brokerKey)

		over := maxXRPDropsPlus1
		set := lending.NewLoanBrokerSet(base.owner.Address, base.vaultID)
		set.DebtMaximum = &over
		requireValueRangeException(t, base.env.Submit(set), base.env, base.owner, beforeState, beforeEntries)

		maximum := maxXRPDrops
		set = lending.NewLoanBrokerSet(base.owner.Address, base.vaultID)
		set.DebtMaximum = &maximum
		jtx.RequireTxSuccess(t, base.env.Submit(set))
		require.True(t, base.env.LedgerEntryExists(brokerKey))
	})

	t.Run("update rejects above native maximum and accepts maximum", func(t *testing.T) {
		f := newValueRangeBroker(t, "XRP")
		beforeState := snapshotValueRangeAccount(f.env, f.lender)
		beforeEntries := snapshotValueRangeEntries(t, f.env, f.vault, f.broker)

		over := maxXRPDropsPlus1
		set := lending.NewLoanBrokerSet(f.lender.Address, f.vaultID)
		set.LoanBrokerID = &f.brokerID
		set.DebtMaximum = &over
		requireValueRangeException(t, f.env.Submit(set), f.env, f.lender, beforeState, beforeEntries)

		maximum := maxXRPDrops
		set = lending.NewLoanBrokerSet(f.lender.Address, f.vaultID)
		set.LoanBrokerID = &f.brokerID
		set.DebtMaximum = &maximum
		jtx.RequireTxSuccess(t, f.env.Submit(set))
		require.Equal(t, beforeState.ownerCount, f.env.OwnerCount(f.lender))
	})
}

func TestLoanSetValueRange(t *testing.T) {
	cases := []struct {
		name      string
		kind      string
		field     string
		principal string
	}{
		{name: "xrp principal", kind: "XRP", field: "PrincipalRequested", principal: maxXRPDropsPlus1},
		// Origination cannot exceed principal, which preclaim checks first.
		{name: "xrp origination fee", kind: "XRP", field: "LoanOriginationFee", principal: maxXRPDropsPlus1},
		{name: "xrp service fee", kind: "XRP", field: "LoanServiceFee", principal: "1000"},
		{name: "xrp late payment fee", kind: "XRP", field: "LatePaymentFee", principal: "1000"},
		{name: "xrp close payment fee", kind: "XRP", field: "ClosePaymentFee", principal: "1000"},
		{name: "mpt principal", kind: "MPT", field: "PrincipalRequested", principal: "1e19"},
		{name: "iou principal", kind: "IOU", field: "PrincipalRequested", principal: "1e96"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newValueRangeBroker(t, tc.kind)
			beforeState := snapshotValueRangeAccount(f.env, f.borrower)
			beforeEntries := snapshotValueRangeEntries(t, f.env, f.relevantEntries()...)
			result := f.env.Submit(signedValueRangeLoan(t, f, tc.principal, tc.field))
			requireValueRangeException(t, result, f.env, f.borrower, beforeState, beforeEntries)
		})
	}
}

func TestLoanSetFractionalValueStillReportsPrecisionLoss(t *testing.T) {
	f := newValueRangeBroker(t, "XRP")
	before := snapshotValueRangeAccount(f.env, f.borrower)
	result := f.env.Submit(signedValueRangeLoan(t, f, "1000.5", "PrincipalRequested"))
	require.Equal(t, ter.TecPRECISION_LOSS.String(), result.Code)
	require.True(t, result.Applied)
	require.Equal(t, uint64(20), result.Fee)
	require.NotNil(t, result.Metadata)
	require.Equal(t, before.balance-result.Fee, f.env.Balance(f.borrower))
	require.Equal(t, before.sequence+1, f.env.Seq(f.borrower))
	require.Equal(t, before.ownerCount, f.env.OwnerCount(f.borrower))
}
