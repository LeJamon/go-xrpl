package check_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	checkbuilder "github.com/LeJamon/go-xrpl/internal/testing/check"
	"github.com/LeJamon/go-xrpl/internal/testing/metadata"
	"github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

const (
	checkCashFlowDeletedHighMetadataSHA256 = "49B8E61D7371B1E32FA5B55BB365685B1DAFDE118DF17863A9FEE970369717C8"
	checkCashFlowDeletedHighStateRoot      = "D6761E1369188ABEDC67AD0CEF8173EEA25F806C4CAEF92F92236EDAA5896225"
	checkCashFlowDeletedHighTxRoot         = "3DA8C496A5EEE71529D01118B69B8D976AFDBFEE5A46CAA4522DC86248E2D825"
	checkCashFlowDeletedLowMetadataSHA256  = "96E73BC07AAD7F7033A853052B8A38C19872E023109E89E07D50E7D2000DA0DB"
	checkCashFlowDeletedLowStateRoot       = "264D98529D8E669076AFAB9F5AC8DD8DCC3A2C3A0B827F626B09FEE4F76F6A08"
	checkCashFlowDeletedLowTxRoot          = "E434097AF3DE53DE2A81EF1CBF6740481E1387770D9C76A05B60867F56F9C69C"
)

func TestCheckCashExactAmountSkipsRestoreWhenFlowDeletesTrustLine(t *testing.T) {
	t.Run("high limit", func(t *testing.T) {
		testCheckCashFlowDeletedLine(t,
			"check-cash-line-issuer", "check-cash-line-casher", "HighLimit",
			checkCashFlowDeletedHighMetadataSHA256, checkCashFlowDeletedHighStateRoot, checkCashFlowDeletedHighTxRoot)
	})
	t.Run("low limit", func(t *testing.T) {
		testCheckCashFlowDeletedLine(t,
			"check-cash-line-casher", "check-cash-line-issuer", "LowLimit",
			checkCashFlowDeletedLowMetadataSHA256, checkCashFlowDeletedLowStateRoot, checkCashFlowDeletedLowTxRoot)
	})
}

func testCheckCashFlowDeletedLine(t *testing.T, issuerSeed, casherSeed, limitField, metadataSHA, expectedStateRoot, txRoot string) {
	t.Helper()
	env := jtx.NewTestEnv(t)
	issuer := jtx.NewAccount(issuerSeed)
	casher := jtx.NewAccount(casherSeed)
	env.Fund(issuer, casher)
	env.Close()

	xrp := tx.NewXRPAmount(10_000)
	casherUSD := tx.NewIssuedAmountFromFloat64(900, "USD", casher.Address)
	jtx.RequireTxSuccess(t, env.Submit(offer.OfferCreate(issuer, casherUSD, xrp).Build()))
	env.Close()
	jtx.RequireTxSuccess(t, env.Submit(offer.OfferCreate(casher, xrp, casherUSD).Build()))
	env.Close()

	lineKey := keylet.Line(casher.ID, issuer.ID, "USD")
	jtx.RequireLedgerEntryExists(t, env, lineKey)
	lineData, err := env.LedgerEntry(lineKey)
	require.NoError(t, err)
	line, err := state.ParseRippleState(lineData)
	require.NoError(t, err)
	require.True(t, line.HasLowNode)
	require.True(t, line.HasHighNode)
	require.Zero(t, line.LowNode)
	require.Zero(t, line.HighNode)
	jtx.RequireOwnerDirectoryContains(t, env, issuer, lineKey.Key, true)
	jtx.RequireOwnerDirectoryContains(t, env, casher, lineKey.Key, true)
	jtx.RequireOwnerCount(t, env, issuer, 1)
	jtx.RequireOwnerCount(t, env, casher, 0)

	issuerUSD := tx.NewIssuedAmountFromFloat64(900, "USD", issuer.Address)
	checkSequence := env.Seq(issuer)
	checkKey := keylet.Check(issuer.ID, checkSequence)
	checkID := strings.ToUpper(hex.EncodeToString(checkKey.Key[:]))
	jtx.RequireTxSuccess(t, env.Submit(checkbuilder.CheckCreate(issuer, casher, issuerUSD).Build()))
	env.Close()
	checkData, err := env.LedgerEntry(checkKey)
	require.NoError(t, err)
	storedCheck, err := state.ParseCheck(checkData)
	require.NoError(t, err)
	require.Zero(t, storedCheck.OwnerNode)
	require.True(t, storedCheck.HasDestNode)
	require.Zero(t, storedCheck.DestinationNode)
	jtx.RequireOwnerDirectoryContains(t, env, issuer, checkKey.Key, true)
	jtx.RequireOwnerDirectoryContains(t, env, casher, checkKey.Key, true)
	jtx.RequireOwnerCount(t, env, issuer, 2)

	result := env.Submit(checkbuilder.CheckCashAmount(casher, checkID, issuerUSD).Build())
	jtx.RequireTxSuccess(t, result)
	requireDeliveredAmount(t, result, issuerUSD)

	requireDeletedLedgerNode(t, result, "Check", checkKey)
	deletedLine := requireDeletedLedgerNode(t, result, "RippleState", lineKey)
	if limitField == "LowLimit" {
		require.Less(t, state.CompareAccountIDs(casher.ID, issuer.ID), 0)
	} else {
		require.Greater(t, state.CompareAccountIDs(casher.ID, issuer.ID), 0)
	}
	limit, ok := deletedLine.FinalFields[limitField].(map[string]any)
	require.True(t, ok, "%s must be an issued amount", limitField)
	require.Equal(t, "9999999999999999e80", limit["value"])
	require.Equal(t, "USD", limit["currency"])
	require.Equal(t, issuer.Address, limit["issuer"])
	jtx.RequireLedgerEntryNotExists(t, env, checkKey)
	jtx.RequireLedgerEntryNotExists(t, env, lineKey)
	jtx.RequireOwnerDirectoryContains(t, env, issuer, checkKey.Key, false)
	jtx.RequireOwnerDirectoryContains(t, env, casher, checkKey.Key, false)
	jtx.RequireOwnerDirectoryContains(t, env, issuer, lineKey.Key, false)
	jtx.RequireOwnerDirectoryContains(t, env, casher, lineKey.Key, false)
	jtx.RequireOwnerCount(t, env, issuer, 0)
	jtx.RequireOwnerCount(t, env, casher, 0)
	requireEmptyOwnerDirectory(t, env, issuer)
	requireEmptyOwnerDirectory(t, env, casher)

	metadataBlob, err := tx.SerializeMetadata(result.Metadata)
	require.NoError(t, err)
	metadataHash := sha256.Sum256(metadataBlob)
	env.Close()
	stateRoot, err := env.LastClosedLedger().StateMapHash()
	require.NoError(t, err)
	transactionRoot, err := env.LastClosedLedger().TxMapHash()
	require.NoError(t, err)
	require.Equal(t, metadataSHA, strings.ToUpper(hex.EncodeToString(metadataHash[:])))
	require.Equal(t, expectedStateRoot, strings.ToUpper(hex.EncodeToString(stateRoot[:])))
	require.Equal(t, txRoot, strings.ToUpper(hex.EncodeToString(transactionRoot[:])))
}

func requireEmptyOwnerDirectory(t *testing.T, env *jtx.TestEnv, owner *jtx.Account) {
	t.Helper()
	directoryKey := keylet.OwnerDir(owner.ID)
	data, err := env.LedgerEntry(directoryKey)
	require.NoError(t, err)
	directory, err := state.ParseDirectoryNode(data)
	require.NoError(t, err)
	require.Equal(t, owner.ID, directory.Owner)
	require.Equal(t, directoryKey.Key, directory.RootIndex)
	require.Empty(t, directory.Indexes)
	require.Zero(t, directory.IndexNext)
	require.Zero(t, directory.IndexPrevious)
}

func requireDeletedLedgerNode(t *testing.T, result jtx.TxResult, entryType string, key keylet.Keylet) *tx.AffectedNode {
	t.Helper()
	node := metadata.FindNode(result.Metadata, "DeletedNode", entryType)
	require.NotNil(t, node, "deleted %s metadata", entryType)
	require.Equal(t, strings.ToUpper(hex.EncodeToString(key.Key[:])), node.LedgerIndex)
	return node
}
