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
	checkCashFlowDeletedLineMetadataSHA256 = "B464AAAEC99A18A9CB14DC6087A696BBED6F425D8E31D08BEFF4DB4140C60022"
	checkCashFlowDeletedLineStateRoot      = "D6761E1369188ABEDC67AD0CEF8173EEA25F806C4CAEF92F92236EDAA5896225"
	checkCashFlowDeletedLineTxRoot         = "666A42DCCC9A798FF82619D0D5A117E283401D37D407B26C0920014803084675"
)

func TestCheckCashExactAmountSkipsRestoreWhenFlowDeletesTrustLine(t *testing.T) {
	env := jtx.NewTestEnv(t)
	issuer := jtx.NewAccount("check-cash-line-issuer")
	casher := jtx.NewAccount("check-cash-line-casher")
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
	requireDeletedLedgerNode(t, result, "RippleState", lineKey)
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
	require.Equal(t, checkCashFlowDeletedLineMetadataSHA256, strings.ToUpper(hex.EncodeToString(metadataHash[:])))
	require.Equal(t, checkCashFlowDeletedLineStateRoot, strings.ToUpper(hex.EncodeToString(stateRoot[:])))
	require.Equal(t, checkCashFlowDeletedLineTxRoot, strings.ToUpper(hex.EncodeToString(transactionRoot[:])))
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

func requireDeletedLedgerNode(t *testing.T, result jtx.TxResult, entryType string, key keylet.Keylet) {
	t.Helper()
	node := metadata.FindNode(result.Metadata, "DeletedNode", entryType)
	require.NotNil(t, node, "deleted %s metadata", entryType)
	require.Equal(t, strings.ToUpper(hex.EncodeToString(key.Key[:])), node.LedgerIndex)
}
