package sponsor_test

import (
	"bytes"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	paymenttx "github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func affectedNodeByKey(metadata *tx.Metadata, entryKey keylet.Keylet) *tx.AffectedNode {
	index := strings.ToUpper(hex.EncodeToString(entryKey.Key[:]))
	for i := range metadata.AffectedNodes {
		if strings.EqualFold(metadata.AffectedNodes[i].LedgerIndex, index) {
			return &metadata.AffectedNodes[i]
		}
	}
	return nil
}

func TestPaymentFeeSponsorDestinationNetZero(t *testing.T) {
	testCases := []struct {
		name   string
		amount uint64
	}{
		{name: "sponsor loses one drop", amount: 9},
		{name: "sponsor is unchanged", amount: 10},
		{name: "sponsor gains one drop", amount: 11},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			env, source, _, sponsor, _ := sponsorEnv(t)
			sourceKey := keylet.Account(source.ID)
			sponsorKey := keylet.Account(sponsor.ID)

			sourceBalanceBefore := env.Balance(source)
			sourceSequenceBefore := env.Seq(source)
			sourceBefore := accountState(t, env, source)
			sponsorBalanceBefore := env.Balance(sponsor)
			sponsorSequenceBefore := env.Seq(sponsor)
			sponsorBefore := accountState(t, env, sponsor)
			sponsorBytesBefore, err := env.LedgerEntry(sponsorKey)
			require.NoError(t, err)
			ledgerSequence := env.LedgerSeq()

			payment := paymenttx.NewPayment(
				source.Address,
				sponsor.Address,
				tx.NewXRPAmount(int64(testCase.amount)),
			)
			payment.Fee = "10"
			payment.Sponsor = sponsor.Address
			flags := tx.SpfSponsorFee
			payment.SponsorFlags = &flags
			attachSponsorSignature(t, env, payment, source, sponsor)

			result := env.SubmitSigned(payment)
			require.Equal(t, "tesSUCCESS", result.Code)
			require.True(t, result.Applied)
			require.Equal(t, uint64(10), result.Fee)
			require.NotNil(t, result.Metadata)

			txHash, err := tx.ComputeTransactionHash(payment)
			require.NoError(t, err)

			require.Equal(t, sourceBalanceBefore-testCase.amount, env.Balance(source))
			require.Equal(t, sourceSequenceBefore+1, env.Seq(source))
			require.Equal(t, sponsorBalanceBefore+testCase.amount-10, env.Balance(sponsor))
			require.Equal(t, sponsorSequenceBefore, env.Seq(sponsor))

			sourceAfter := accountState(t, env, source)
			sponsorAfter := accountState(t, env, sponsor)
			sponsorBytesAfter, err := env.LedgerEntry(sponsorKey)
			require.NoError(t, err)
			sourceNode := affectedNodeByKey(result.Metadata, sourceKey)
			sponsorNode := affectedNodeByKey(result.Metadata, sponsorKey)

			require.Equal(t, txHash, sourceAfter.PreviousTxnID)
			require.Equal(t, ledgerSequence, sourceAfter.PreviousTxnLgrSeq)
			require.NotNil(t, sourceNode)
			require.Equal(t, "ModifiedNode", sourceNode.NodeType)
			require.Equal(t, "AccountRoot", sourceNode.LedgerEntryType)
			require.Equal(t, strings.ToUpper(hex.EncodeToString(sourceBefore.PreviousTxnID[:])), sourceNode.PreviousTxnID)
			require.Equal(t, sourceBefore.PreviousTxnLgrSeq, sourceNode.PreviousTxnLgrSeq)
			require.Equal(t, strconv.FormatUint(sourceBalanceBefore, 10), sourceNode.PreviousFields["Balance"])
			require.Equal(t, strconv.FormatUint(sourceBalanceBefore-testCase.amount, 10), sourceNode.FinalFields["Balance"])

			if testCase.amount == 10 {
				require.Len(t, result.Metadata.AffectedNodes, 1)
				require.True(t, bytes.Equal(sponsorBytesBefore, sponsorBytesAfter))
				require.Equal(t, sponsorBefore.PreviousTxnID, sponsorAfter.PreviousTxnID)
				require.Equal(t, sponsorBefore.PreviousTxnLgrSeq, sponsorAfter.PreviousTxnLgrSeq)
				require.Nil(t, sponsorNode, "byte-identical sponsor must not appear in metadata")
				return
			}

			require.Len(t, result.Metadata.AffectedNodes, 2)
			require.False(t, bytes.Equal(sponsorBytesBefore, sponsorBytesAfter))
			require.Equal(t, txHash, sponsorAfter.PreviousTxnID)
			require.Equal(t, ledgerSequence, sponsorAfter.PreviousTxnLgrSeq)
			require.NotNil(t, sponsorNode)
			require.Equal(t, "ModifiedNode", sponsorNode.NodeType)
			require.Equal(t, "AccountRoot", sponsorNode.LedgerEntryType)
			require.Equal(t, strings.ToUpper(hex.EncodeToString(sponsorBefore.PreviousTxnID[:])), sponsorNode.PreviousTxnID)
			require.Equal(t, sponsorBefore.PreviousTxnLgrSeq, sponsorNode.PreviousTxnLgrSeq)
			require.Equal(t, strconv.FormatUint(sponsorBalanceBefore, 10), sponsorNode.PreviousFields["Balance"])
			require.Equal(t, strconv.FormatUint(sponsorBalanceBefore+testCase.amount-10, 10), sponsorNode.FinalFields["Balance"])
		})
	}
}

func TestPaymentDelegateDestinationNetZero(t *testing.T) {
	env, source, _, _, delegate := sponsorEnv(t)
	grantDelegatePermission(t, env, source, delegate, "Payment")

	sourceKey := keylet.Account(source.ID)
	delegateKey := keylet.Account(delegate.ID)
	sourceBalanceBefore := env.Balance(source)
	sourceSequenceBefore := env.Seq(source)
	delegateBalanceBefore := env.Balance(delegate)
	delegateSequenceBefore := env.Seq(delegate)
	delegateBefore := accountState(t, env, delegate)
	delegateBytesBefore, err := env.LedgerEntry(delegateKey)
	require.NoError(t, err)
	ledgerSequence := env.LedgerSeq()

	payment := paymenttx.NewPayment(source.Address, delegate.Address, tx.NewXRPAmount(10))
	payment.Fee = "10"
	payment.Delegate = delegate.Address

	result := env.SubmitSignedWith(payment, delegate)
	require.Equal(t, "tesSUCCESS", result.Code)
	require.True(t, result.Applied)
	require.Equal(t, uint64(10), result.Fee)
	require.NotNil(t, result.Metadata)
	require.Len(t, result.Metadata.AffectedNodes, 1)

	txHash, err := tx.ComputeTransactionHash(payment)
	require.NoError(t, err)
	require.Equal(t, sourceBalanceBefore-10, env.Balance(source))
	require.Equal(t, sourceSequenceBefore+1, env.Seq(source))
	require.Equal(t, txHash, accountState(t, env, source).PreviousTxnID)
	require.Equal(t, ledgerSequence, accountState(t, env, source).PreviousTxnLgrSeq)
	sourceNode := affectedNodeByKey(result.Metadata, sourceKey)
	require.NotNil(t, sourceNode)
	require.Equal(t, "ModifiedNode", sourceNode.NodeType)
	require.Equal(t, "AccountRoot", sourceNode.LedgerEntryType)

	delegateAfter := accountState(t, env, delegate)
	delegateBytesAfter, err := env.LedgerEntry(delegateKey)
	require.NoError(t, err)
	require.Equal(t, delegateBalanceBefore, env.Balance(delegate))
	require.Equal(t, delegateSequenceBefore, env.Seq(delegate))
	require.True(t, bytes.Equal(delegateBytesBefore, delegateBytesAfter))
	require.Equal(t, delegateBefore.PreviousTxnID, delegateAfter.PreviousTxnID)
	require.Equal(t, delegateBefore.PreviousTxnLgrSeq, delegateAfter.PreviousTxnLgrSeq)
	require.Nil(t, affectedNodeByKey(result.Metadata, delegateKey))
}

func TestPaymentFeeSponsorDestinationNetZeroClearsPasswordSpent(t *testing.T) {
	env, source, _, sponsor, _ := sponsorEnv(t)
	sourceKey := keylet.Account(source.ID)
	sponsorKey := keylet.Account(sponsor.ID)

	sponsorRoot := accountState(t, env, sponsor)
	sponsorRoot.Flags |= state.LsfPasswordSpent
	sponsorData, err := state.SerializeAccountRoot(sponsorRoot)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(sponsorKey, sponsorData))

	sourceBalanceBefore := env.Balance(source)
	sourceSequenceBefore := env.Seq(source)
	sponsorBalanceBefore := env.Balance(sponsor)
	sponsorSequenceBefore := env.Seq(sponsor)
	sponsorBefore := accountState(t, env, sponsor)
	sponsorBytesBefore, err := env.LedgerEntry(sponsorKey)
	require.NoError(t, err)
	ledgerSequence := env.LedgerSeq()

	payment := paymenttx.NewPayment(source.Address, sponsor.Address, tx.NewXRPAmount(10))
	payment.Fee = "10"
	payment.Sponsor = sponsor.Address
	flags := tx.SpfSponsorFee
	payment.SponsorFlags = &flags
	attachSponsorSignature(t, env, payment, source, sponsor)

	result := env.SubmitSigned(payment)
	require.Equal(t, "tesSUCCESS", result.Code)
	require.True(t, result.Applied)
	require.Equal(t, uint64(10), result.Fee)
	require.NotNil(t, result.Metadata)
	require.Len(t, result.Metadata.AffectedNodes, 2)

	txHash, err := tx.ComputeTransactionHash(payment)
	require.NoError(t, err)
	require.Equal(t, sourceBalanceBefore-10, env.Balance(source))
	require.Equal(t, sourceSequenceBefore+1, env.Seq(source))
	sourceNode := affectedNodeByKey(result.Metadata, sourceKey)
	require.NotNil(t, sourceNode)
	require.Equal(t, "ModifiedNode", sourceNode.NodeType)
	require.Equal(t, "AccountRoot", sourceNode.LedgerEntryType)

	sponsorAfter := accountState(t, env, sponsor)
	sponsorBytesAfter, err := env.LedgerEntry(sponsorKey)
	require.NoError(t, err)
	require.Equal(t, sponsorBalanceBefore, env.Balance(sponsor))
	require.Equal(t, sponsorSequenceBefore, env.Seq(sponsor))
	require.Zero(t, sponsorAfter.Flags&state.LsfPasswordSpent)
	require.False(t, bytes.Equal(sponsorBytesBefore, sponsorBytesAfter))
	require.Equal(t, txHash, sponsorAfter.PreviousTxnID)
	require.Equal(t, ledgerSequence, sponsorAfter.PreviousTxnLgrSeq)

	sponsorNode := affectedNodeByKey(result.Metadata, sponsorKey)
	require.NotNil(t, sponsorNode)
	require.Equal(t, "ModifiedNode", sponsorNode.NodeType)
	require.Equal(t, "AccountRoot", sponsorNode.LedgerEntryType)
	require.Equal(t, strings.ToUpper(hex.EncodeToString(sponsorBefore.PreviousTxnID[:])), sponsorNode.PreviousTxnID)
	require.Equal(t, sponsorBefore.PreviousTxnLgrSeq, sponsorNode.PreviousTxnLgrSeq)
	require.EqualValues(t, sponsorBefore.Flags, sponsorNode.PreviousFields["Flags"])
	require.EqualValues(t, sponsorAfter.Flags, sponsorNode.FinalFields["Flags"])
	require.NotContains(t, sponsorNode.PreviousFields, "Balance")
}
