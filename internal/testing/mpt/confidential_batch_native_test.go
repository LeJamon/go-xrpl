//go:build mptcrypto && cgo

package mpt_test

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/crypto/mptcrypto"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	batchtest "github.com/LeJamon/go-xrpl/internal/testing/batch"
	"github.com/LeJamon/go-xrpl/internal/tx"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	mpttx "github.com/LeJamon/go-xrpl/internal/tx/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

func confidentialBatchKeyPair(t *testing.T) ([32]byte, []byte) {
	t.Helper()
	private, public, ok := mptcrypto.GenerateKeyPair()
	require.True(t, ok)
	return private, public
}

func confidentialBatchBlind(t *testing.T) [32]byte {
	t.Helper()
	blind, ok := mptcrypto.GenerateBlindingFactor()
	require.True(t, ok)
	return blind
}

func confidentialBatchEncrypt(t *testing.T, amount uint64, public []byte, blind [32]byte) []byte {
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
