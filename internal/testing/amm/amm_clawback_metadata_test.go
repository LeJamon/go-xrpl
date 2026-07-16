package amm_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

func accountRootMetadataNode(meta *tx.Metadata, accountID [20]byte) *tx.AffectedNode {
	key := keylet.Account(accountID).Key
	ledgerIndex := strings.ToUpper(hex.EncodeToString(key[:]))
	for i := range meta.AffectedNodes {
		node := &meta.AffectedNodes[i]
		if node.LedgerEntryType == "AccountRoot" && node.LedgerIndex == ledgerIndex {
			return node
		}
	}
	return nil
}

func TestAMMClawbackIOUPoolEmitsBareThreadedAMMAccount(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	gw2 := jtx.NewAccount("gw2")
	env.EnableOpenLedgerReplay()
	env.FundAmount(env.GW, uint64(jtx.XRP(1_000_000)))
	env.FundAmount(gw2, uint64(jtx.XRP(1_000_000)))
	env.FundAmount(env.Alice, uint64(jtx.XRP(1_000_000)))
	env.FundAmount(env.Bob, uint64(jtx.XRP(1_000_000)))
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(env.GW).AllowClawback().Build()))
	env.Close()

	eur := tx.Asset{Currency: "EUR", Issuer: gw2.Address}
	env.Trust(env.Alice, env.GW, "USD", 100_000)
	env.PayIOU(env.GW, env.Alice, "USD", 3_000)
	env.Trust(env.Alice, gw2, "EUR", 100_000)
	env.PayIOU(gw2, env.Alice, "EUR", 3_000)
	env.Trust(env.Bob, env.GW, "USD", 100_000)
	env.PayIOU(env.GW, env.Bob, "USD", 3_000)
	env.Trust(env.Bob, gw2, "EUR", 100_000)
	env.PayIOU(gw2, env.Bob, "EUR", 3_000)
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(amm.AMMCreate(
		env.Alice,
		amm.IOUAmount(gw2, "EUR", 1_000),
		amm.IOUAmount(env.GW, "USD", 2_000),
	).Build()))
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(amm.AMMDeposit(env.Bob, env.USD, eur).
		Amount(amm.IOUAmount(env.GW, "USD", 2_000)).
		Amount2(amm.IOUAmount(gw2, "EUR", 1_000)).
		TwoAsset().
		Build()))
	env.Close()

	ammAccount := amm.AMMAccount(t, env, env.USD, eur)
	clawbackTx := amm.AMMClawback(env.GW, env.Bob.Address, env.USD, eur).Build()
	result := env.Submit(clawbackTx)
	jtx.RequireTxSuccess(t, result)
	require.NotNil(t, result.Metadata)

	node := accountRootMetadataNode(result.Metadata, ammAccount.ID)
	require.NotNil(t, node, "affected nodes: %+v", result.Metadata.AffectedNodes)
	require.Equal(t, "ModifiedNode", node.NodeType)
	require.NotEmpty(t, node.PreviousTxnID)
	require.NotZero(t, node.PreviousTxnLgrSeq)
	require.Nil(t, node.FinalFields)
	require.Nil(t, node.PreviousFields)

	metadataBlob, err := tx.SerializeMetadata(result.Metadata)
	require.NoError(t, err)
	require.Len(t, metadataBlob, 2_248)
	metadataHash := sha512half.Sum(metadataBlob)
	require.Equal(t,
		"5BAB776D2BE53B806FA36CEB802C2E20331D1784855EB7506EFDF1C8622B8058",
		strings.ToUpper(hex.EncodeToString(metadataHash[:])))
	txBlob, err := tx.SerializeTransaction(clawbackTx)
	require.NoError(t, err)
	txWithMeta, err := tx.CreateTxWithMetaBlob(txBlob, result.Metadata)
	require.NoError(t, err)
	txHash, err := tx.ComputeTransactionHash(clawbackTx)
	require.NoError(t, err)
	txMap := shamap.New(shamap.TypeTransaction)
	require.NoError(t, txMap.PutWithNodeType(txHash, txWithMeta, shamap.NodeTypeTransactionWithMeta))
	txRoot, err := txMap.Hash()
	require.NoError(t, err)
	require.Equal(t,
		"E1BDF44F57FC43AE2CD9609009B80613FEF0430B0D03F71D3A4B433F5197FDEC",
		strings.ToUpper(hex.EncodeToString(txRoot[:])))
}

func TestAMMClawbackXRPPoolPersistsAMMAccountBalance(t *testing.T) {
	env := setupClawbackEnvWithUSD(t, 1_000_000, 1_000_000, 3_000)

	jtx.RequireTxSuccess(t, env.Submit(amm.AMMCreate(
		env.Alice,
		amm.XRPAmount(1_000),
		amm.IOUAmount(env.GW, "USD", 2_000),
	).Build()))
	env.Close()

	ammAccount := amm.AMMAccount(t, env, env.USD, amm.XRP())
	balanceBefore := env.Balance(ammAccount)
	result := env.Submit(amm.AMMClawback(env.GW, env.Alice.Address, env.USD, amm.XRP()).
		Amount(amm.IOUAmount(env.GW, "USD", 1_000)).
		Build())
	jtx.RequireTxSuccess(t, result)

	require.Equal(t, balanceBefore-uint64(jtx.XRP(500)), env.Balance(ammAccount))
	node := accountRootMetadataNode(result.Metadata, ammAccount.ID)
	require.NotNil(t, node)
	require.NotEmpty(t, node.FinalFields)
	require.Contains(t, node.PreviousFields, "Balance")
}
