package jtx

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
	delegatetx "github.com/LeJamon/go-xrpl/internal/tx/delegate"
	"github.com/LeJamon/go-xrpl/internal/tx/offer"
	paymenttx "github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubLedgerEntryReader struct {
	exists    bool
	existsErr error
	data      []byte
	readErr   error
}

func (r stubLedgerEntryReader) Exists(keylet.Keylet) (bool, error) {
	return r.exists, r.existsErr
}

func (r stubLedgerEntryReader) Read(keylet.Keylet) ([]byte, error) {
	return r.data, r.readErr
}

func TestNewAccount(t *testing.T) {
	// Test deterministic account creation
	alice1 := NewAccount("alice")
	alice2 := NewAccount("alice")

	// Same name should produce same account
	assert.Equal(t, alice1.Address, alice2.Address)
	assert.Equal(t, alice1.PublicKey, alice2.PublicKey)
	assert.Equal(t, alice1.PrivateKey, alice2.PrivateKey)

	// Different name should produce different account
	bob := NewAccount("bob")
	assert.NotEqual(t, alice1.Address, bob.Address)
}

func TestNewAccountWithKeyType(t *testing.T) {
	// Test secp256k1
	aliceSecp := NewAccountWithKeyType("alice", KeyTypeSecp256k1)
	assert.True(t, aliceSecp.IsSecp256k1())
	assert.False(t, aliceSecp.IsEd25519())

	// Test ed25519
	aliceEd := NewAccountWithKeyType("alice", KeyTypeEd25519)
	assert.True(t, aliceEd.IsEd25519())
	assert.False(t, aliceEd.IsSecp256k1())

	// Different key types should produce different addresses
	assert.NotEqual(t, aliceSecp.Address, aliceEd.Address)
}

func TestMasterAccount(t *testing.T) {
	master := MasterAccount()

	// Should be the well-known genesis account
	assert.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", master.Address)
	assert.Equal(t, "master", master.Name)
}

func TestAccountHuman(t *testing.T) {
	alice := NewAccount("alice")

	// Human() should return the address
	assert.Equal(t, alice.Address, alice.Human())
}

func TestAccountString(t *testing.T) {
	alice := NewAccount("alice")

	// String() should include name and address
	str := alice.String()
	assert.Contains(t, str, "alice")
	assert.Contains(t, str, alice.Address)
}

func TestXRPConversion(t *testing.T) {
	// 1 XRP = 1,000,000 drops
	assert.Equal(t, int64(1_000_000), XRP(1))
	assert.Equal(t, int64(100_000_000), XRP(100))
	assert.Equal(t, int64(1_000_000_000_000), XRP(1_000_000))
}

func TestDropsConversion(t *testing.T) {
	// Drops should pass through unchanged
	assert.Equal(t, int64(1000), Drops(1000))
	assert.Equal(t, int64(0), Drops(0))
}

func TestManualClock(t *testing.T) {
	clock := NewManualClock()

	// Default time should be Jan 1, 2020
	now := clock.Now()
	assert.Equal(t, 2020, now.Year())
	assert.Equal(t, time.January, now.Month())
	assert.Equal(t, 1, now.Day())

	// Advance time
	clock.Advance(10 * time.Second)
	now2 := clock.Now()
	assert.Equal(t, 10*time.Second, now2.Sub(now))

	// Set time
	newTime := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	clock.Set(newTime)
	assert.Equal(t, newTime, clock.Now())
}

func TestTxResult(t *testing.T) {
	success := txResultFromTER(ter.TesSUCCESS, false)
	assert.True(t, success.IsSuccess())
	assert.False(t, success.IsClaimed())
	assert.False(t, success.IsRetry())
	assert.False(t, success.IsMalformed())
	assert.False(t, success.IsFailed())

	claimed := txResultFromTER(ter.TecUNFUNDED_PAYMENT, false)
	assert.False(t, claimed.IsSuccess())
	assert.True(t, claimed.IsClaimed())
	assert.False(t, claimed.IsRetry())

	// Retry (ter)
	retry := txResultFromTER(ter.TerPRE_SEQ, false)
	assert.False(t, retry.IsSuccess())
	assert.True(t, retry.IsRetry())

	malformed := txResultFromTER(ter.TemMALFORMED, false)
	assert.False(t, malformed.IsSuccess())
	assert.True(t, malformed.IsMalformed())

	failed := txResultFromTER(ter.TefPAST_SEQ, false)
	assert.False(t, failed.IsSuccess())
	assert.True(t, failed.IsFailed())

	queued := txResultFromTER(ter.TerQUEUED, true)
	assert.True(t, queued.Queued)
	assert.False(t, queued.Applied)
}

func TestRegisterAccountRejectsIdentityCollisions(t *testing.T) {
	env := NewTestEnv(t)
	alice := NewAccount("alice")
	require.NoError(t, env.registerAccount(alice))
	require.NoError(t, env.registerAccount(NewAccount("alice")))

	nameCollision := NewAccount("bob")
	nameCollision.Name = alice.Name
	require.Error(t, env.registerAccount(nameCollision))
	require.Same(t, alice, env.accounts[alice.Name])
	require.Same(t, alice, env.accountsByAddress[alice.Address])

	credentialCollision := *alice
	credentialCollision.Name = "alice-alias"
	credentialCollision.PrivateKey = append([]byte(nil), alice.PrivateKey...)
	credentialCollision.PrivateKey[0] ^= 0xff
	require.Error(t, env.registerAccount(&credentialCollision))
	require.NotContains(t, env.accounts, credentialCollision.Name)
	require.Same(t, alice, env.accounts[alice.Name])
	require.Same(t, alice, env.findAccountByAddress(alice.Address))
}

func TestSetAmendmentsClonesAndCanClear(t *testing.T) {
	env := NewTestEnv(t)
	names := []string{"BatchV1_1"}
	env.SetAmendments(names)
	names[0] = "XRPFees"
	require.True(t, env.FeatureEnabled("BatchV1_1"))
	require.False(t, env.FeatureEnabled("XRPFees"))
	env.Close()
	require.True(t, env.FeatureEnabled("BatchV1_1"))

	env.SetAmendments(nil)
	require.False(t, env.FeatureEnabled("BatchV1_1"))
	env.Close()
	require.False(t, env.FeatureEnabled("BatchV1_1"))
}

func TestMultiSignFee(t *testing.T) {
	tests := []struct {
		name        string
		current     string
		signerCount int
		baseFee     uint64
		want        string
		wantErr     bool
	}{
		{name: "malformed", current: "ten", signerCount: 1, baseFee: 10, wantErr: true},
		{name: "negative", current: "-1", signerCount: 1, baseFee: 10, wantErr: true},
		{name: "decimal", current: "1.5", signerCount: 1, baseFee: 10, wantErr: true},
		{name: "fee overflow", current: "18446744073709551616", signerCount: 1, baseFee: 10, wantErr: true},
		{name: "multiplication overflow", current: "1", signerCount: math.MaxInt, baseFee: 3, wantErr: true},
		{name: "exact", current: "30", signerCount: 2, baseFee: 10, want: "30"},
		{name: "below", current: "20", signerCount: 2, baseFee: 10, want: "30"},
		{name: "above", current: "40", signerCount: 2, baseFee: 10, want: "40"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := multiSignFee(test.current, test.signerCount, test.baseFee)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestSubmitAutofill(t *testing.T) {
	env := NewTestEnv(t)
	alice := NewAccount("alice")
	destination := NewAccount("destination")
	env.Fund(alice, destination)
	env.Close()
	env.SetNetworkID(1025)

	transaction := accounttx.NewAccountDelete(alice.Address, destination.Address)
	env.autoFill(transaction, SubmitOptions{})
	common := transaction.GetCommon()
	require.Equal(t, "10", common.Fee)
	require.NotNil(t, common.Sequence)
	require.Equal(t, env.Seq(alice), *common.Sequence)
	require.NotNil(t, common.NetworkID)
	require.Equal(t, uint32(1025), *common.NetworkID)
	require.Equal(t, alice.PublicKeyHex(), common.SigningPubKey)
	require.Equal(t, "00", common.TxnSignature)

	ticketSequence := uint32(7)
	ticketWithoutFunclet := accounttx.NewAccountSet(alice.Address)
	ticketWithoutFunclet.TicketSequence = &ticketSequence
	env.autoFill(ticketWithoutFunclet, SubmitOptions{})
	require.NotNil(t, ticketWithoutFunclet.Sequence)
	require.Equal(t, env.Seq(alice), *ticketWithoutFunclet.Sequence)

	ticketWithFunclet := WithTicketSeq(accounttx.NewAccountSet(alice.Address), ticketSequence)
	env.autoFill(ticketWithFunclet, SubmitOptions{})
	require.NotNil(t, ticketWithFunclet.GetCommon().Sequence)
	require.Zero(t, *ticketWithFunclet.GetCommon().Sequence)

	raw := accounttx.NewAccountDelete(alice.Address, destination.Address)
	env.autoFill(raw, SubmitOptions{
		SkipFee:       true,
		SkipSequence:  true,
		SkipNetworkID: true,
		SkipSignature: true,
	})
	require.Empty(t, raw.Fee)
	require.Nil(t, raw.Sequence)
	require.Nil(t, raw.NetworkID)
	require.Empty(t, raw.SigningPubKey)
	require.Empty(t, raw.TxnSignature)
}

func TestVerifiedSubmitAutofillsRegularKey(t *testing.T) {
	env := NewTestEnv(t)
	alice := NewAccount("alice")
	regularKey := NewAccount("regular-key")
	env.Fund(alice, regularKey)
	env.SetRegularKey(alice, regularKey)
	env.DisableMasterKey(alice)
	env.SetVerifySignatures(true)

	transaction := accounttx.NewAccountSet(alice.Address)
	result := env.Submit(transaction)
	require.Equal(t, ter.TesSUCCESS, result.Result)
	require.True(t, result.Applied)
	require.Equal(t, regularKey.PublicKeyHex(), transaction.SigningPubKey)
}

func TestAppliedTransactionPopulatesLedgerMap(t *testing.T) {
	env := NewTestEnv(t)
	alice := NewAccount("alice")
	env.Fund(alice)
	env.Close()

	transaction := accounttx.NewAccountSet(alice.Address)
	result := env.Submit(transaction)
	require.True(t, result.Applied)
	require.NotNil(t, result.Metadata)
	hash, err := tx.ComputeTransactionHash(transaction)
	require.NoError(t, err)
	exists, err := env.Ledger().TxExists(hash)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, uint32(1), env.Ledger().TxCount())

	env.Close()
	closed := env.LastClosedLedger()
	require.Equal(t, uint32(1), closed.TxCount())
	root, err := closed.TxMapHash()
	require.NoError(t, err)
	require.NotEqual(t, [32]byte{}, root)
	require.Equal(t, root, closed.Header().TxHash)
}

func TestCloseIncludesNewlyApplicableHeldTransaction(t *testing.T) {
	env := NewTestEnv(t)
	alice := NewAccount("alice")
	env.Fund(alice)
	env.Close()
	env.EnableOpenLedgerReplay()

	sequence := env.Seq(alice)
	heldSequence := sequence + 1
	held := accounttx.NewAccountSet(alice.Address)
	held.Sequence = &heldSequence
	result := env.Submit(held)
	require.Equal(t, ter.TerPRE_SEQ, result.Result)
	require.False(t, result.Applied)
	heldHash, err := tx.ComputeTransactionHash(held)
	require.NoError(t, err)

	first := accounttx.NewAccountSet(alice.Address)
	first.Sequence = &sequence
	require.Equal(t, ter.TesSUCCESS, env.Submit(first).Result)
	env.Close()

	exists, err := env.LastClosedLedger().TxExists(heldHash)
	require.NoError(t, err)
	require.True(t, exists)
}

func TestCloseAppliesHeldTransactionsBeforeStagedAmendments(t *testing.T) {
	env := NewTestEnv(t)
	env.EnableFeatureNow("PermissionDelegationV1_1")
	alice := NewAccount("alice")
	bob := NewAccount("bob")
	env.Fund(alice, bob)
	env.Close()
	require.True(t, env.FeatureEnabled("PermissionDelegationV1_1"))

	held := delegatetx.NewDelegateSet(alice.Address)
	held.Authorize = bob.Address
	held.Permissions = append(held.Permissions, delegatetx.NewPermission("Payment"))
	env.autoFill(held, SubmitOptions{})
	env.addHeldTransaction(alice.Address, held)
	heldHash, err := tx.ComputeTransactionHash(held)
	require.NoError(t, err)

	env.SetAmendments(nil)
	env.Close()

	exists, err := env.LastClosedLedger().TxExists(heldHash)
	require.NoError(t, err)
	require.True(t, exists)
	require.False(t, env.FeatureEnabled("PermissionDelegationV1_1"))
}

func TestSignedSubmitUsesTxQ(t *testing.T) {
	config := txq.DefaultConfig()
	config.MinimumTxnInLedger = 2
	config.TargetTxnInLedger = 2
	config.QueueSizeMin = 2
	env := NewTestEnvWithTxQ(t, config)
	alice := NewAccount("alice")
	bob := NewAccount("bob")
	env.FundAmountNoRipple(alice, uint64(XRP(1000)))
	env.FundAmountNoRipple(bob, uint64(XRP(1000)))
	env.Close()

	for range env.TxQMetrics().TxPerLedger + 1 {
		env.Noop(alice)
	}
	result := env.SubmitSigned(accounttx.NewAccountSet(bob.Address))
	require.Equal(t, ter.TerQUEUED, result.Result)
	require.True(t, result.Queued)
	require.False(t, result.Applied)
	require.Equal(t, uint64(1), env.TxQMetrics().TxCount)
}

func TestTxQDoesNotRetryHeldTransactionAfterTec(t *testing.T) {
	env := NewTestEnvWithTxQ(t, txq.StandaloneConfig())
	alice := NewAccount("alice")
	env.Fund(alice)
	env.Close()

	sequence := env.Seq(alice)
	heldSequence := sequence + 1
	held := accounttx.NewAccountSet(alice.Address)
	held.Sequence = &heldSequence
	require.Equal(t, ter.TerPRE_SEQ, env.Submit(held).Result)

	unfunded := NewAccount("unfunded")
	claimed := paymenttx.NewPayment(alice.Address, unfunded.Address, XRPTxAmount(1))
	result := env.Submit(claimed)
	require.Equal(t, ter.TecNO_DST_INSUF_XRP, result.Result)
	require.True(t, result.Applied)
	require.Equal(t, sequence+1, env.Seq(alice))

	heldHash, err := tx.ComputeTransactionHash(held)
	require.NoError(t, err)
	exists, err := env.Ledger().TxExists(heldHash)
	require.NoError(t, err)
	require.False(t, exists)
}

func TestTxQRetainsAndSweepsLocalFailure(t *testing.T) {
	env := NewTestEnvWithTxQ(t, txq.StandaloneConfig())
	alice := NewAccount("alice")
	env.Fund(alice)
	env.Close()

	pastSequence := env.Seq(alice) - 1
	obsolete := accounttx.NewAccountSet(alice.Address)
	obsolete.Sequence = &pastSequence
	require.Equal(t, ter.TefPAST_SEQ, env.Submit(obsolete).Result)
	require.Equal(t, 1, env.localTxs.Size())

	env.Close()
	require.Zero(t, env.localTxs.Size())
}

func TestFeeSettingsStayInSync(t *testing.T) {
	modernConfig := genesis.DefaultConfig()
	modernConfig.Amendments = append(modernConfig.Amendments, amendment.FeatureXRPFees)
	modern := NewTestEnvWithConfig(t, modernConfig)
	modern.SetBaseFee(20)
	modern.SetReserves(30, 40)
	require.Equal(t, uint64(20), modern.BaseFee())
	require.Equal(t, uint64(30), modern.ReserveBase())
	require.Equal(t, uint64(40), modern.ReserveIncrement())
	data, err := modern.Ledger().Read(keylet.Fees())
	require.NoError(t, err)
	settings, err := state.ParseFeeSettings(data)
	require.NoError(t, err)
	require.True(t, settings.XRPFeesMode)
	require.Equal(t, uint64(20), settings.BaseFeeDrops)
	require.Equal(t, uint64(30), settings.ReserveBaseDrops)
	require.Equal(t, uint64(40), settings.ReserveIncrementDrops)

	legacyConfig := genesis.DefaultConfig()
	legacyConfig.Amendments = nil
	legacyConfig.Fees = genesis.DefaultFees{
		BaseFee:          drops.NewXRPAmount(10),
		ReserveBase:      drops.NewXRPAmount(200_000_000),
		ReserveIncrement: drops.NewXRPAmount(50_000_000),
	}
	legacy := NewTestEnvWithConfig(t, legacyConfig)
	legacy.SetBaseFee(25)
	legacy.SetReserves(35, 45)
	data, err = legacy.Ledger().Read(keylet.Fees())
	require.NoError(t, err)
	settings, err = state.ParseFeeSettings(data)
	require.NoError(t, err)
	require.False(t, settings.XRPFeesMode)
	require.Equal(t, uint64(25), settings.BaseFee)
	require.Equal(t, uint32(35), settings.ReserveBase)
	require.Equal(t, uint32(45), settings.ReserveIncrement)

	before := append([]byte(nil), data...)
	err = legacy.writeFeeSettings(25, uint64(math.MaxUint32)+1, 45)
	require.Error(t, err)
	after, readErr := legacy.Ledger().Read(keylet.Fees())
	require.NoError(t, readErr)
	require.Equal(t, before, after)
}

func TestOwnerDirectoryTraversalFailsClosed(t *testing.T) {
	corruptEnv := NewTestEnv(t)
	corruptOwner := NewAccount("corrupt-owner")
	require.NoError(t, corruptEnv.Ledger().Insert(keylet.OwnerDir(corruptOwner.ID), make([]byte, 12)))
	err := ownerDirectoryForEach(corruptEnv, corruptOwner, func([32]byte) error { return nil })
	require.Error(t, err)

	env := NewTestEnv(t)
	owner := NewAccount("owner")
	rootKey := keylet.OwnerDir(owner.ID)
	root := &state.DirectoryNode{RootIndex: rootKey.Key}
	root.SetIndexNext(5)
	data, err := state.SerializeDirectoryNode(root, false)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Insert(rootKey, data))

	err = ownerDirectoryForEach(env, owner, func([32]byte) error { return nil })
	require.ErrorContains(t, err, "continuation page 5 is missing")

	page := &state.DirectoryNode{RootIndex: rootKey.Key}
	page.SetIndexNext(5)
	data, err = state.SerializeDirectoryNode(page, false)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Insert(keylet.OwnerDirPage(owner.ID, 5), data))
	err = ownerDirectoryForEach(env, owner, func([32]byte) error { return nil })
	require.ErrorContains(t, err, "cycle at page 5")

	item := [32]byte{1}
	root.Indexes = [][32]byte{item}
	root.IndexNext = 0
	data, err = state.SerializeDirectoryNode(root, false)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(rootKey, data))
	wantErr := errors.New("callback failed")
	err = ownerDirectoryForEach(env, owner, func([32]byte) error { return wantErr })
	require.ErrorIs(t, err, wantErr)
}

func TestDirectoryBumpIsAtomic(t *testing.T) {
	env := NewTestEnv(t)
	owner := NewAccount("owner")
	rootKey := keylet.OwnerDir(owner.ID)
	missingEntry := [32]byte{7}
	root := &state.DirectoryNode{RootIndex: rootKey.Key, Owner: owner.ID}
	root.SetIndexNext(2)
	root.SetIndexPrevious(2)
	rootData, err := state.SerializeDirectoryNode(root, false)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Insert(rootKey, rootData))
	last := &state.DirectoryNode{
		RootIndex: rootKey.Key,
		Owner:     owner.ID,
		Indexes:   [][32]byte{missingEntry},
	}
	lastData, err := state.SerializeDirectoryNode(last, false)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Insert(keylet.DirPage(rootKey.Key, 2), lastData))

	before, err := env.Ledger().StateMapHash()
	require.NoError(t, err)
	err = env.BumpDirectoryLastPage(owner, 5, "OwnerNode")
	require.ErrorContains(t, err, "moved entry")
	after, hashErr := env.Ledger().StateMapHash()
	require.NoError(t, hashErr)
	require.Equal(t, before, after)
	exists, existsErr := env.Ledger().Exists(keylet.DirPage(rootKey.Key, 5))
	require.NoError(t, existsErr)
	require.False(t, exists)

	require.NoError(t, env.Ledger().Insert(keylet.DirPage(rootKey.Key, 5), lastData))
	before, err = env.Ledger().StateMapHash()
	require.NoError(t, err)
	err = env.BumpDirectoryLastPage(owner, 5, "OwnerNode")
	require.ErrorContains(t, err, "already exists")
	after, hashErr = env.Ledger().StateMapHash()
	require.NoError(t, hashErr)
	require.Equal(t, before, after)
}

func TestDirectoryBumpRejectsAmbiguousNodeFields(t *testing.T) {
	owner := NewAccount("owner")
	destination := NewAccount("destination")
	data, err := state.SerializePayChannelFromData(&state.PayChannelData{
		Account:         owner.ID,
		DestinationID:   destination.ID,
		Amount:          1,
		SettleDelay:     1,
		PublicKey:       owner.PublicKeyHex(),
		OwnerNode:       2,
		DestinationNode: 2,
		HasDestNode:     true,
	})
	require.NoError(t, err)
	_, err = updateDirNodeFields(data, "", 2, 5)
	require.ErrorContains(t, err, "multiple directory node fields")
}

func TestLedgerEntryReadersFailClosed(t *testing.T) {
	alice := NewAccount("alice")
	lineKey := keylet.Line(alice.ID, NewAccount("bob").ID, "USD")

	account, exists, err := readAccountRoot(stubLedgerEntryReader{}, alice.ID)
	require.NoError(t, err)
	require.False(t, exists)
	require.Nil(t, account)

	rippleState, exists, err := readRippleState(stubLedgerEntryReader{}, lineKey)
	require.NoError(t, err)
	require.False(t, exists)
	require.Nil(t, rippleState)

	readers := map[string]stubLedgerEntryReader{
		"exists error": {existsErr: errors.New("exists failed")},
		"read error":   {exists: true, readErr: errors.New("read failed")},
		"missing data": {exists: true},
		"malformed":    {exists: true, data: []byte{0xff}},
	}
	for name, reader := range readers {
		t.Run(name, func(t *testing.T) {
			_, _, accountErr := readAccountRoot(reader, alice.ID)
			require.Error(t, accountErr)
			_, _, lineErr := readRippleState(reader, lineKey)
			require.Error(t, lineErr)
		})
	}
}

func TestIssuedCurrencyHelpers(t *testing.T) {
	gateway := NewAccount("gateway")

	// USD
	usd := USD(gateway, 100.50)
	assert.Equal(t, "USD", usd.Currency)
	assert.Equal(t, gateway.Address, usd.Issuer)
	assert.InDelta(t, 100.5, usd.Float64(), 0.0001)

	// EUR
	eur := EUR(gateway, 50.0)
	assert.Equal(t, "EUR", eur.Currency)
	assert.Equal(t, gateway.Address, eur.Issuer)

	// BTC
	btc := BTC(gateway, 0.001)
	assert.Equal(t, "BTC", btc.Currency)
	assert.Equal(t, gateway.Address, btc.Issuer)

	// Custom currency
	jpy := IssuedCurrency(gateway, "JPY", 1000.0)
	assert.Equal(t, "JPY", jpy.Currency)
	assert.Equal(t, gateway.Address, jpy.Issuer)

	zero := IssuedCurrency(gateway, "USD", 0.5e-6)
	assert.Zero(t, zero.IOU().Mantissa())
	assert.Equal(t, -100, zero.IOU().Exponent())
	assert.True(t, IssuedCurrency(gateway, "USD", 0.499999e-6).IsZero())
	assert.Equal(t, 0.000001, IssuedCurrency(gateway, "USD", 0.500001e-6).Float64())
	assert.Equal(t, -1.234568, IssuedCurrency(gateway, "USD", -1.2345678).Float64())
	assert.Equal(t, IssuedCurrency(gateway, "USD", 1.2345678), gateway.IOU("USD", 1.2345678))
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), math.MaxFloat64} {
		assert.Panics(t, func() { IssuedCurrency(gateway, "USD", value) })
	}
}

func TestXRPBoundaries(t *testing.T) {
	maxWhole := math.MaxInt64 / DropsPerXRP
	minWhole := math.MinInt64 / DropsPerXRP
	assert.Equal(t, maxWhole*DropsPerXRP, XRP(maxWhole))
	assert.Equal(t, minWhole*DropsPerXRP, XRP(minWhole))
	assert.Panics(t, func() { XRP(maxWhole + 1) })
	assert.Panics(t, func() { XRP(minWhole - 1) })
}

func TestXRPTxAmount(t *testing.T) {
	amount := XRPTxAmount(1_000_000)
	assert.True(t, amount.IsNative())
	assert.Equal(t, int64(1000000), amount.Drops())
}

func TestXRPTxAmountFromXRP(t *testing.T) {
	amount := XRPTxAmountFromXRP(100.0)
	assert.True(t, amount.IsNative())
	assert.Equal(t, int64(100000000), amount.Drops())
	assert.Equal(t, int64(1), XRPTxAmountFromXRP(0.000001).Drops())
	assert.Equal(t, int64(-1), XRPTxAmountFromXRP(-0.000001).Drops())
	assert.Equal(t, int64(math.MinInt64), XRPTxAmountFromXRP(float64(math.MinInt64)/float64(DropsPerXRP)).Drops())
	for _, value := range []float64{
		0.0000005,
		-0.0000005,
		1.0000001,
		math.NaN(),
		math.Inf(1),
		math.Inf(-1),
		float64(math.MaxInt64) / float64(DropsPerXRP),
	} {
		assert.Panics(t, func() { XRPTxAmountFromXRP(value) })
	}
}

// TestRequireLinesCountsRippleStateOnly verifies that RequireLines counts
// RippleState entries in the owner directory rather than approximating with
// OwnerCount: an offer raises OwnerCount but must not count as a trust line,
// and both sides of a line see it through their respective directories.
func TestRequireLinesCountsRippleStateOnly(t *testing.T) {
	env := NewTestEnv(t)
	gw := NewAccount("gateway")
	alice := NewAccount("alice")
	env.Fund(gw)
	env.Fund(alice)

	RequireLines(t, env, alice, 0)
	RequireOffers(t, env, alice, 0)

	env.Trust(alice, USD(gw, 100))
	RequireLines(t, env, alice, 1)
	// The gateway's owner directory references the same RippleState entry.
	RequireLines(t, env, gw, 1)

	oc := offer.NewOfferCreate(alice.Address, XRPTxAmount(XRP(10)), USD(gw, 10))
	oc.Fee = formatUint64(env.BaseFee())
	seq := env.Seq(alice)
	oc.Sequence = &seq
	if alice.PublicKey != nil {
		env.SignWith(oc, alice)
	}
	RequireTxSuccess(t, env.Submit(oc))

	// Alice now owns a trust line and an offer: OwnerCount is 2 but the
	// per-type counts must stay distinct.
	RequireLines(t, env, alice, 1)
	RequireOffers(t, env, alice, 1)
}

// TestNewTestEnv tests the basic TestEnv creation
// This test requires the ledger and genesis packages to be properly implemented
func TestNewTestEnv(t *testing.T) {
	env := NewTestEnv(t)
	require.NotNil(t, env)

	// Should have master account registered
	master := env.MasterAccount()
	require.NotNil(t, master)
	assert.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", master.Address)

	// Should start at ledger sequence 2
	assert.Equal(t, uint32(2), env.LedgerSeq())

	// Should have default fees
	assert.Equal(t, uint64(10), env.BaseFee())
	assert.Equal(t, uint64(200_000_000), env.ReserveBase())
	assert.Equal(t, uint64(50_000_000), env.ReserveIncrement())
}
