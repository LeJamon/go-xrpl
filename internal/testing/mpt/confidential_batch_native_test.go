//go:build mptcrypto && cgo

package mpt_test

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/crypto/mptcrypto"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/testing"
	batchtest "github.com/LeJamon/go-xrpl/internal/testing/batch"
	"github.com/LeJamon/go-xrpl/internal/tx"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	mpttx "github.com/LeJamon/go-xrpl/internal/tx/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

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

func batchBlind(t *testing.T) [32]byte {
	t.Helper()
	blind, ok := mptcrypto.GenerateBlindingFactor()
	require.True(t, ok)
	return blind
}

func batchEncrypt(t *testing.T, amount uint64, public []byte, blind [32]byte) []byte {
	t.Helper()
	ciphertext, ok := mptcrypto.EncryptAmount(amount, public, blind)
	require.True(t, ok)
	return ciphertext
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
	env.EnableFeatureNow("BatchV1_1")
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
