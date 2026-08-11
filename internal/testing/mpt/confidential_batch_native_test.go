//go:build mptcrypto && cgo

package mpt_test

import (
	"encoding/hex"
	"math/big"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/crypto/mptcrypto"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	batchtest "github.com/LeJamon/go-xrpl/internal/testing/batch"
	credentialtest "github.com/LeJamon/go-xrpl/internal/testing/credential"
	"github.com/LeJamon/go-xrpl/internal/tx"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	mpttx "github.com/LeJamon/go-xrpl/internal/tx/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

func confidentialBatchKeyPair(t *testing.T) ([32]byte, []byte) {
	return batchKeyPair(t)
}

type confidentialBatchFixture struct {
	env            *jtx.TestEnv
	issuer         *jtx.Account
	sender         *jtx.Account
	destination    *jtx.Account
	id             [24]byte
	senderKey      keylet.Keylet
	senderPrivate  [32]byte
	senderPublic   []byte
	destinationPub []byte
	issuerPublic   []byte
	balanceBlind   [32]byte
}

func batchKeyPair(t *testing.T) ([32]byte, []byte) {
	t.Helper()
	private, public, ok := mptcrypto.GenerateKeyPair()
	require.True(t, ok)
	return private, public
}

func confidentialBatchBlind(t *testing.T) [32]byte {
	return batchBlind(t)
}

func batchBlind(t *testing.T) [32]byte {
	t.Helper()
	blind, ok := mptcrypto.GenerateBlindingFactor()
	require.True(t, ok)
	return blind
}

func confidentialBatchEncrypt(t *testing.T, amount uint64, public []byte, blind [32]byte) []byte {
	return batchEncrypt(t, amount, public, blind)
}

func batchEncrypt(t *testing.T, amount uint64, public []byte, blind [32]byte) []byte {
	t.Helper()
	ciphertext, ok := mptcrypto.EncryptAmount(amount, public, blind)
	require.True(t, ok)
	return ciphertext
}

func confidentialBatchHex(value []byte) string { return hex.EncodeToString(value) }

func TestConfidentialMPTBatchAllOrNothingRollsBackStaleProof(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeatureNow("Batch")
	env.EnableFeatureNow("ConfidentialTransfer")
	holder := jtx.NewAccount("confidential-holder")
	issuer := jtx.NewAccount("confidential-issuer")
	env.FundAmount(holder, uint64(jtx.XRP(1000)))
	env.FundAmount(issuer, uint64(jtx.XRP(1000)))

	holderPrivate, holderPublic := confidentialBatchKeyPair(t)
	_, issuerPublic := confidentialBatchKeyPair(t)
	id := keylet.MakeMPTID(3, issuer.ID)
	balanceBlind := confidentialBatchBlind(t)
	spending := confidentialBatchEncrypt(t, 40, holderPublic, balanceBlind)
	issuerBalance := confidentialBatchEncrypt(t, 40, issuerPublic, balanceBlind)
	inbox, ok := mptcrypto.CanonicalZero(holderPublic, holder.ID, id)
	require.True(t, ok)

	issuance := &state.MPTokenIssuanceData{
		Issuer: issuer.ID, Sequence: 3, OutstandingAmount: 100, ConfidentialOutstandingAmount: 40,
		Flags: entry.LsfMPTCanHoldConfidentialBalance, IssuerEncryptionKey: issuerPublic,
	}
	token := &state.MPTokenData{
		Account: holder.ID, MPTokenIssuanceID: id, MPTAmount: 60,
		HolderEncryptionKey: holderPublic, ConfidentialBalanceInbox: inbox,
		ConfidentialBalanceSpending: spending, IssuerEncryptedBalance: issuerBalance,
		ConfidentialBalanceVersion: 3,
	}
	issuanceData, err := state.SerializeMPTokenIssuance(issuance)
	require.NoError(t, err)
	tokenData, err := state.SerializeMPToken(token)
	require.NoError(t, err)
	issuanceKey := keylet.MPTIssuance(id)
	tokenKey := keylet.MPToken(issuanceKey.Key, holder.ID)
	require.NoError(t, env.Ledger().Insert(issuanceKey, issuanceData))
	require.NoError(t, env.Ledger().Insert(tokenKey, tokenData))

	outerSequence := env.Seq(holder)
	makeBack := func(sequence uint32) *mpttx.ConfidentialMPTConvertBack {
		amountBlind := confidentialBatchBlind(t)
		commitment, valid := mptcrypto.PedersenCommitment(40, balanceBlind)
		require.True(t, valid)
		contextHash, valid := mptcrypto.ConvertBackContext(holder.ID, id, sequence, 3)
		require.True(t, valid)
		proof, valid := mptcrypto.GenerateConvertBackProof(
			holderPrivate, holderPublic, contextHash, 10, commitment, 40, spending, balanceBlind,
		)
		require.True(t, valid)
		transaction := &mpttx.ConfidentialMPTConvertBack{
			BaseTx:                *tx.NewBaseTx(tx.TypeConfidentialMPTConvertBack, holder.Address),
			MPTokenIssuanceID:     confidentialBatchHex(id[:]),
			MPTAmount:             10,
			HolderEncryptedAmount: confidentialBatchHex(confidentialBatchEncrypt(t, 10, holderPublic, amountBlind)),
			IssuerEncryptedAmount: confidentialBatchHex(confidentialBatchEncrypt(t, 10, issuerPublic, amountBlind)),
			BlindingFactor:        confidentialBatchHex(amountBlind[:]),
			ZKProof:               confidentialBatchHex(proof),
			BalanceCommitment:     confidentialBatchHex(commitment),
		}
		transaction.Fee = "0"
		transaction.SigningPubKey = ""
		transaction.SetSequence(sequence)
		transaction.SetFlags(tx.TfInnerBatchTxn)
		return transaction
	}

	batch := batchtest.NewBatchBuilder(
		holder, outerSequence, 22*env.BaseFee(), batchtx.BatchFlagAllOrNothing,
	).AddInnerTx(makeBack(outerSequence + 1)).
		AddInnerTx(makeBack(outerSequence + 2)).
		Build()
	result := env.Submit(batch)
	require.Equal(t, ter.TesSUCCESS, result.Result)
	require.Empty(t, result.AppliedInnerTransactions)

	gotIssuanceData, err := env.Ledger().Read(issuanceKey)
	require.NoError(t, err)
	gotIssuance, err := state.ParseMPTokenIssuance(gotIssuanceData)
	require.NoError(t, err)
	gotTokenData, err := env.Ledger().Read(tokenKey)
	require.NoError(t, err)
	gotToken, err := state.ParseMPToken(gotTokenData)
	require.NoError(t, err)
	require.Equal(t, uint64(40), gotIssuance.ConfidentialOutstandingAmount)
	require.Equal(t, uint64(60), gotToken.MPTAmount)
	require.Equal(t, uint32(3), gotToken.ConfidentialBalanceVersion)
	require.Equal(t, spending, gotToken.ConfidentialBalanceSpending)
}

func batchZero(t *testing.T, public []byte, account [20]byte, id [24]byte) []byte {
	t.Helper()
	zero, ok := mptcrypto.CanonicalZero(public, account, id)
	require.True(t, ok)
	return zero
}

func newConfidentialBatchFixture(t *testing.T) *confidentialBatchFixture {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeatureNow("ConfidentialTransfer")
	env.EnableFeatureNow("Batch")
	issuer := jtx.NewAccount("confidential-batch-issuer")
	sender := jtx.NewAccount("confidential-batch-sender")
	destination := jtx.NewAccount("confidential-batch-destination")
	env.Fund(issuer, sender, destination)
	env.Close()

	senderPrivate, senderPublic := batchKeyPair(t)
	_, destinationPublic := batchKeyPair(t)
	_, issuerPublic := batchKeyPair(t)
	balanceBlind := batchBlind(t)
	id := keylet.MakeMPTID(4, issuer.ID)
	issuance := &state.MPTokenIssuanceData{
		Issuer: issuer.ID, Sequence: 4,
		Flags:                         entry.LsfMPTCanTransfer | entry.LsfMPTCanHoldConfidentialBalance,
		OutstandingAmount:             100,
		ConfidentialOutstandingAmount: 100,
		IssuerEncryptionKey:           issuerPublic,
	}
	senderToken := &state.MPTokenData{
		Account:                     sender.ID,
		MPTokenIssuanceID:           id,
		HolderEncryptionKey:         senderPublic,
		ConfidentialBalanceInbox:    batchZero(t, senderPublic, sender.ID, id),
		ConfidentialBalanceSpending: batchEncrypt(t, 100, senderPublic, balanceBlind),
		IssuerEncryptedBalance:      batchEncrypt(t, 100, issuerPublic, balanceBlind),
		ConfidentialBalanceVersion:  9,
	}
	destinationToken := &state.MPTokenData{
		Account:                     destination.ID,
		MPTokenIssuanceID:           id,
		HolderEncryptionKey:         destinationPublic,
		ConfidentialBalanceInbox:    batchZero(t, destinationPublic, destination.ID, id),
		ConfidentialBalanceSpending: batchZero(t, destinationPublic, destination.ID, id),
		IssuerEncryptedBalance:      batchZero(t, issuerPublic, destination.ID, id),
		ConfidentialBalanceVersion:  3,
	}
	issuanceData, err := state.SerializeMPTokenIssuance(issuance)
	require.NoError(t, err)
	senderData, err := state.SerializeMPToken(senderToken)
	require.NoError(t, err)
	destinationData, err := state.SerializeMPToken(destinationToken)
	require.NoError(t, err)
	issuanceKey := keylet.MPTIssuance(id)
	senderKey := keylet.MPToken(issuanceKey.Key, sender.ID)
	destinationKey := keylet.MPToken(issuanceKey.Key, destination.ID)
	require.NoError(t, env.Ledger().Insert(issuanceKey, issuanceData))
	require.NoError(t, env.Ledger().Insert(senderKey, senderData))
	require.NoError(t, env.Ledger().Insert(destinationKey, destinationData))

	return &confidentialBatchFixture{
		env: env, issuer: issuer, sender: sender, destination: destination, id: id,
		senderKey: senderKey, senderPrivate: senderPrivate, senderPublic: senderPublic,
		destinationPub: destinationPublic, issuerPublic: issuerPublic, balanceBlind: balanceBlind,
	}
}

func (f *confidentialBatchFixture) send(t *testing.T, sequence uint32, version uint32, balance uint64, spending []byte, balanceBlind [32]byte, amount uint64, amountBlind [32]byte) *mpttx.ConfidentialMPTSend {
	t.Helper()
	participants := []mptcrypto.Participant{
		{PublicKey: f.senderPublic, Ciphertext: batchEncrypt(t, amount, f.senderPublic, amountBlind)},
		{PublicKey: f.destinationPub, Ciphertext: batchEncrypt(t, amount, f.destinationPub, amountBlind)},
		{PublicKey: f.issuerPublic, Ciphertext: batchEncrypt(t, amount, f.issuerPublic, amountBlind)},
	}
	amountCommitment, ok := mptcrypto.PedersenCommitment(amount, amountBlind)
	require.True(t, ok)
	balanceCommitment, ok := mptcrypto.PedersenCommitment(balance, balanceBlind)
	require.True(t, ok)
	proofContext, ok := mptcrypto.SendContext(f.sender.ID, f.id, sequence, f.destination.ID, version)
	require.True(t, ok)
	proof, ok := mptcrypto.GenerateSendProof(f.senderPrivate, amount, participants, amountBlind, proofContext, amountCommitment, balanceCommitment, balance, spending, balanceBlind)
	require.True(t, ok)
	transaction := &mpttx.ConfidentialMPTSend{
		BaseTx:                     *tx.NewBaseTx(tx.TypeConfidentialMPTSend, f.sender.Address),
		MPTokenIssuanceID:          hex.EncodeToString(f.id[:]),
		Destination:                f.destination.Address,
		SenderEncryptedAmount:      hex.EncodeToString(participants[0].Ciphertext),
		DestinationEncryptedAmount: hex.EncodeToString(participants[1].Ciphertext),
		IssuerEncryptedAmount:      hex.EncodeToString(participants[2].Ciphertext),
		ZKProof:                    hex.EncodeToString(proof),
		AmountCommitment:           hex.EncodeToString(amountCommitment),
		BalanceCommitment:          hex.EncodeToString(balanceCommitment),
	}
	transaction.Fee = "0"
	transaction.SigningPubKey = ""
	transaction.SetSequence(sequence)
	transaction.SetFlags(tx.TfInnerBatchTxn)
	return transaction
}

func subtractBlind(a, b [32]byte) [32]byte {
	order, _ := new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141", 16)
	value := new(big.Int).Sub(new(big.Int).SetBytes(a[:]), new(big.Int).SetBytes(b[:]))
	value.Mod(value, order)
	var result [32]byte
	value.FillBytes(result[:])
	return result
}

func readBatchHolding(t *testing.T, env *jtx.TestEnv, key keylet.Keylet) *state.MPTokenData {
	t.Helper()
	data, err := env.LedgerEntry(key)
	require.NoError(t, err)
	holding, err := state.ParseMPToken(data)
	require.NoError(t, err)
	return holding
}

func TestConfidentialSendBatchStaleProofModes(t *testing.T) {
	for _, test := range []struct {
		name         string
		flag         uint32
		wantVersion  uint32
		wantSequence uint32
		wantApplied  int
	}{
		{name: "all or nothing rolls back the first send", flag: batchtx.BatchFlagAllOrNothing, wantVersion: 9, wantSequence: 1, wantApplied: 0},
		{name: "independent retains the first send", flag: batchtx.BatchFlagIndependent, wantVersion: 10, wantSequence: 3, wantApplied: 2},
		{name: "until failure stops after the stale proof", flag: batchtx.BatchFlagUntilFailure, wantVersion: 10, wantSequence: 3, wantApplied: 2},
		{name: "only one accepts exactly one successful send", flag: batchtx.BatchFlagOnlyOne, wantVersion: 10, wantSequence: 2, wantApplied: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newConfidentialBatchFixture(t)
			outerSequence := fixture.env.Seq(fixture.sender)
			initial := readBatchHolding(t, fixture.env, fixture.senderKey)
			first := fixture.send(t, outerSequence+1, initial.ConfidentialBalanceVersion, 100, initial.ConfidentialBalanceSpending, fixture.balanceBlind, 40, batchBlind(t))
			second := fixture.send(t, outerSequence+2, initial.ConfidentialBalanceVersion, 100, initial.ConfidentialBalanceSpending, fixture.balanceBlind, 40, batchBlind(t))
			batchFee := uint64(220)
			batch := batchtest.NewBatchBuilder(fixture.sender, outerSequence, batchFee, test.flag).AddInnerTx(first).AddInnerTx(second).Build()
			require.Equal(t, batchFee, batch.CalculateMinimumFee(fixture.env.Ledger(), tx.EngineConfig{BaseFee: fixture.env.BaseFee()}))
			result := fixture.env.Submit(batch)
			jtx.RequireTxSuccess(t, result)
			require.Len(t, result.AppliedInnerTransactions, test.wantApplied)
			if test.flag == batchtx.BatchFlagIndependent {
				require.Equal(t, ter.TesSUCCESS, result.AppliedInnerTransactions[0].Metadata.TransactionResult)
				require.Equal(t, ter.TecBAD_PROOF, result.AppliedInnerTransactions[1].Metadata.TransactionResult)
				require.NotNil(t, result.AppliedInnerTransactions[0].Metadata.ParentBatchID)
				require.NotNil(t, result.AppliedInnerTransactions[1].Metadata.ParentBatchID)
			}
			updated := readBatchHolding(t, fixture.env, fixture.senderKey)
			require.Equal(t, test.wantVersion, updated.ConfidentialBalanceVersion)
			require.Equal(t, outerSequence+test.wantSequence, fixture.env.Seq(fixture.sender))
		})
	}
}

func TestConfidentialSendBatchOuterTicketUsesInnerSequenceContext(t *testing.T) {
	fixture := newConfidentialBatchFixture(t)
	outerTicket := fixture.env.CreateTickets(fixture.sender, 1)
	fixture.env.Close()
	innerSequence := fixture.env.Seq(fixture.sender)
	initial := readBatchHolding(t, fixture.env, fixture.senderKey)
	firstBlind := batchBlind(t)
	first := fixture.send(t, innerSequence, initial.ConfidentialBalanceVersion, 100, initial.ConfidentialBalanceSpending, fixture.balanceBlind, 40, firstBlind)
	firstAmount, _ := hex.DecodeString(first.SenderEncryptedAmount)
	predictedSpending, ok := mptcrypto.SubtractCiphertexts(initial.ConfidentialBalanceSpending, firstAmount)
	require.True(t, ok)
	predictedBlind := subtractBlind(fixture.balanceBlind, firstBlind)
	second := fixture.send(t, innerSequence+1, initial.ConfidentialBalanceVersion+1, 60, predictedSpending, predictedBlind, 20, batchBlind(t))
	batch := batchtest.NewBatchBuilderWithTicket(fixture.sender, outerTicket, 220, batchtx.BatchFlagAllOrNothing).AddInnerTx(first).AddInnerTx(second).Build()
	result := fixture.env.Submit(batch)
	jtx.RequireTxSuccess(t, result)
	require.Len(t, result.AppliedInnerTransactions, 2)
	require.Equal(t, uint32(11), readBatchHolding(t, fixture.env, fixture.senderKey).ConfidentialBalanceVersion)
}

func TestConfidentialMPTSendDelegatedExecution(t *testing.T) {
	fixture := newConfidentialBatchFixture(t)
	fixture.env.EnableFeature("PermissionDelegationV1_1")
	delegate := jtx.NewAccount("confidential-send-delegate")
	fixture.env.Fund(delegate)
	fixture.env.Close()
	standaloneSend := func(amount uint64, amountBlind [32]byte, balance uint64, balanceBlind [32]byte) *mpttx.ConfidentialMPTSend {
		holding := readBatchHolding(t, fixture.env, fixture.senderKey)
		send := fixture.send(t, fixture.env.Seq(fixture.sender), holding.ConfidentialBalanceVersion, balance, holding.ConfidentialBalanceSpending, balanceBlind, amount, amountBlind)
		send.Flags = nil
		send.Fee = strconv.FormatUint(sign.CalculateBaseFee(send, fixture.env.Ledger(), tx.EngineConfig{BaseFee: fixture.env.BaseFee()}), 10)
		send.Delegate = delegate.Address
		return send
	}

	missingPermission := standaloneSend(40, batchBlind(t), 100, fixture.balanceBlind)
	require.Equal(t, "terNO_DELEGATE_PERMISSION", fixture.env.SubmitSignedWith(missingPermission, delegate).Code)

	fixture.env.SetDelegate(fixture.sender, delegate, []string{"ConfidentialMPTSend"})
	fixture.env.Close()

	initial := readBatchHolding(t, fixture.env, fixture.senderKey)
	amountBlind := batchBlind(t)
	send := standaloneSend(40, amountBlind, 100, fixture.balanceBlind)
	result := fixture.env.SubmitSignedWith(send, delegate)
	jtx.RequireTxSuccess(t, result)
	require.Equal(t, initial.ConfidentialBalanceVersion+1, readBatchHolding(t, fixture.env, fixture.senderKey).ConfidentialBalanceVersion)

	fixture.env.SetDelegate(fixture.sender, delegate, nil)
	fixture.env.Close()
	remainingBlind := subtractBlind(fixture.balanceBlind, amountBlind)
	revoked := standaloneSend(20, batchBlind(t), 60, remainingBlind)
	require.Equal(t, "terNO_DELEGATE_PERMISSION", fixture.env.SubmitSignedWith(revoked, delegate).Code)
}

func TestConfidentialMPTSendExpiredCredentialCleanupPersists(t *testing.T) {
	fixture := newConfidentialBatchFixture(t)
	credentialIssuer := jtx.NewAccount("confidential-send-credential-issuer")
	fixture.env.Fund(credentialIssuer)
	fixture.env.Close()

	const credentialType = "KYC"
	expiration := fixture.env.NowRipple() + 100
	jtx.RequireTxSuccess(t, fixture.env.Submit(credentialtest.CredentialCreateText(credentialIssuer, fixture.sender, credentialType).Expiration(expiration).Build()))
	fixture.env.Close()
	jtx.RequireTxSuccess(t, fixture.env.Submit(credentialtest.CredentialAcceptText(fixture.sender, credentialIssuer, credentialType).Build()))
	fixture.env.CloseToParentCloseTime(expiration + 1)

	initial := readBatchHolding(t, fixture.env, fixture.senderKey)
	send := fixture.send(t, fixture.env.Seq(fixture.sender), initial.ConfidentialBalanceVersion, 100, initial.ConfidentialBalanceSpending, fixture.balanceBlind, 40, batchBlind(t))
	send.Flags = nil
	send.Fee = strconv.FormatUint(sign.CalculateBaseFee(send, fixture.env.Ledger(), tx.EngineConfig{BaseFee: fixture.env.BaseFee()}), 10)
	credentialKey := jtx.CredentialKeylet(fixture.sender, credentialIssuer, credentialType)
	send.CredentialIDs = []string{hex.EncodeToString(credentialKey.Key[:])}
	jtx.RequireTxClaimed(t, fixture.env.Submit(send), jtx.TecEXPIRED)
	require.False(t, fixture.env.LedgerEntryExists(credentialKey))
	require.Equal(t, initial.ConfidentialBalanceVersion, readBatchHolding(t, fixture.env, fixture.senderKey).ConfidentialBalanceVersion)
}

func TestConfidentialClawbackBatchAndFeeDispatch(t *testing.T) {
	fixture := newConfidentialBatchFixture(t)
	issuerPrivate, issuerPublic := batchKeyPair(t)
	holderPublic := fixture.senderPublic
	blind := batchBlind(t)
	issuerCiphertext := batchEncrypt(t, 75, issuerPublic, blind)
	issuanceKey := keylet.MPTIssuance(fixture.id)
	holdingKey := fixture.senderKey
	issuance := &state.MPTokenIssuanceData{Issuer: fixture.issuer.ID, Sequence: 4, Flags: entry.LsfMPTCanClawback | entry.LsfMPTCanHoldConfidentialBalance, OutstandingAmount: 75, ConfidentialOutstandingAmount: 75, IssuerEncryptionKey: issuerPublic}
	holding := &state.MPTokenData{Account: fixture.sender.ID, MPTokenIssuanceID: fixture.id, MPTAmount: 13, HolderEncryptionKey: holderPublic, ConfidentialBalanceInbox: batchEncrypt(t, 50, holderPublic, blind), ConfidentialBalanceSpending: batchEncrypt(t, 25, holderPublic, blind), IssuerEncryptedBalance: issuerCiphertext, ConfidentialBalanceVersion: 6}
	issuanceData, err := state.SerializeMPTokenIssuance(issuance)
	require.NoError(t, err)
	holdingData, err := state.SerializeMPToken(holding)
	require.NoError(t, err)
	require.NoError(t, fixture.env.Ledger().Update(issuanceKey, issuanceData))
	require.NoError(t, fixture.env.Ledger().Update(holdingKey, holdingData))
	outerSequence := fixture.env.Seq(fixture.issuer)
	innerSequence := outerSequence + 1
	proofContext, ok := mptcrypto.ClawbackContext(fixture.issuer.ID, fixture.id, innerSequence, fixture.sender.ID)
	require.True(t, ok)
	proof, ok := mptcrypto.GenerateClawbackProof(issuerPrivate, issuerPublic, proofContext, 75, issuerCiphertext)
	require.True(t, ok)
	clawback := &mpttx.ConfidentialMPTClawback{BaseTx: *tx.NewBaseTx(tx.TypeConfidentialMPTClawback, fixture.issuer.Address), MPTokenIssuanceID: hex.EncodeToString(fixture.id[:]), Holder: fixture.sender.Address, MPTAmount: 75, ZKProof: hex.EncodeToString(proof)}
	clawback.Fee = "0"
	clawback.SigningPubKey = ""
	clawback.SetSequence(innerSequence)
	clawback.SetFlags(tx.TfInnerBatchTxn)
	payment := batchtest.MakeInnerPaymentXRP(fixture.issuer, fixture.destination, 1, innerSequence+1)
	batchFee := uint64(130)
	batch := batchtest.NewBatchBuilder(fixture.issuer, outerSequence, batchFee, batchtx.BatchFlagAllOrNothing).AddInnerTx(clawback).AddInnerTx(payment).Build()
	require.Equal(t, batchFee, batch.CalculateMinimumFee(fixture.env.Ledger(), tx.EngineConfig{BaseFee: fixture.env.BaseFee()}))
	result := fixture.env.Submit(batch)
	jtx.RequireTxSuccess(t, result)
	require.Len(t, result.AppliedInnerTransactions, 2)
	updated := readBatchHolding(t, fixture.env, holdingKey)
	require.Equal(t, uint64(13), updated.MPTAmount)
	require.Equal(t, uint32(7), updated.ConfidentialBalanceVersion)
	updatedIssuanceData, err := fixture.env.LedgerEntry(issuanceKey)
	require.NoError(t, err)
	updatedIssuance, err := state.ParseMPTokenIssuance(updatedIssuanceData)
	require.NoError(t, err)
	require.Zero(t, updatedIssuance.OutstandingAmount)
	require.Zero(t, updatedIssuance.ConfidentialOutstandingAmount)
}

func TestConfidentialMPTClawbackDelegatedExecution(t *testing.T) {
	fixture := newConfidentialBatchFixture(t)
	fixture.env.EnableFeature("PermissionDelegationV1_1")
	delegate := jtx.NewAccount("confidential-clawback-delegate")
	fixture.env.Fund(delegate)
	fixture.env.Close()
	fixture.env.SetDelegate(fixture.sender, delegate, []string{"ConfidentialMPTClawback"})
	fixture.env.Close()

	issuerPrivate, issuerPublic := batchKeyPair(t)
	blind := batchBlind(t)
	issuerCiphertext := batchEncrypt(t, 75, issuerPublic, blind)
	issuanceKey := keylet.MPTIssuance(fixture.id)
	issuance := &state.MPTokenIssuanceData{Issuer: fixture.issuer.ID, Sequence: 4, Flags: entry.LsfMPTCanClawback | entry.LsfMPTCanHoldConfidentialBalance, OutstandingAmount: 75, ConfidentialOutstandingAmount: 75, IssuerEncryptionKey: issuerPublic}
	holding := &state.MPTokenData{Account: fixture.sender.ID, MPTokenIssuanceID: fixture.id, MPTAmount: 13, HolderEncryptionKey: fixture.senderPublic, ConfidentialBalanceInbox: batchEncrypt(t, 50, fixture.senderPublic, blind), ConfidentialBalanceSpending: batchEncrypt(t, 25, fixture.senderPublic, blind), IssuerEncryptedBalance: issuerCiphertext, ConfidentialBalanceVersion: 6}
	issuanceData, err := state.SerializeMPTokenIssuance(issuance)
	require.NoError(t, err)
	holdingData, err := state.SerializeMPToken(holding)
	require.NoError(t, err)
	require.NoError(t, fixture.env.Ledger().Update(issuanceKey, issuanceData))
	require.NoError(t, fixture.env.Ledger().Update(fixture.senderKey, holdingData))

	makeClawback := func() *mpttx.ConfidentialMPTClawback {
		sequence := fixture.env.Seq(fixture.issuer)
		proofContext, ok := mptcrypto.ClawbackContext(fixture.issuer.ID, fixture.id, sequence, fixture.sender.ID)
		require.True(t, ok)
		proof, ok := mptcrypto.GenerateClawbackProof(issuerPrivate, issuerPublic, proofContext, 75, issuerCiphertext)
		require.True(t, ok)
		clawback := &mpttx.ConfidentialMPTClawback{BaseTx: *tx.NewBaseTx(tx.TypeConfidentialMPTClawback, fixture.issuer.Address), MPTokenIssuanceID: hex.EncodeToString(fixture.id[:]), Holder: fixture.sender.Address, MPTAmount: 75, ZKProof: hex.EncodeToString(proof)}
		clawback.SetSequence(sequence)
		clawback.Delegate = delegate.Address
		clawback.Fee = strconv.FormatUint(sign.CalculateBaseFee(clawback, fixture.env.Ledger(), tx.EngineConfig{BaseFee: fixture.env.BaseFee()}), 10)
		return clawback
	}
	require.Equal(t, "terNO_DELEGATE_PERMISSION", fixture.env.SubmitSignedWith(makeClawback(), delegate).Code)

	fixture.env.SetDelegate(fixture.issuer, delegate, []string{"ConfidentialMPTClawback"})
	fixture.env.Close()
	result := fixture.env.SubmitSignedWith(makeClawback(), delegate)
	jtx.RequireTxSuccess(t, result)
	require.Equal(t, uint32(7), readBatchHolding(t, fixture.env, fixture.senderKey).ConfidentialBalanceVersion)
}
