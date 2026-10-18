package batch

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	nfttest "github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/tx"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

type nftBatchReplayEvidence struct {
	LedgerHash   string
	StateRoot    string
	TxRoot       string
	TERResults   []string
	MetadataHash []string
}

type nftBatchReplayRoots struct {
	LedgerHash string
	StateRoot  string
	TxRoot     string
}

func TestNFTBatchReplayParity(t *testing.T) {
	env := newBatchEnv(t)
	seller := jtx.NewAccount("nft-batch-replay-seller")
	buyer := jtx.NewAccount("nft-batch-replay-buyer")
	env.FundAmount(seller, uint64(jtx.XRP(100_000)))
	env.FundAmount(buyer, uint64(jtx.XRP(100_000)))
	env.EnableOpenLedgerReplay()
	env.Close()

	const nftCount = 33
	for range nftCount {
		result := env.Submit(nfttest.NFTokenMint(seller, 0).
			Transferable().
			Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	}

	parentRoots := captureNFTBatchReplayRoots(t, env)
	t.Logf("parent roots: %+v", parentRoots)
	require.Equal(t, nftBatchReplayRoots{
		LedgerHash: "eb60e98aedf09bb4d57c7add7131ca599e6fe4d40da18309fd5d969ce1db48d4",
		StateRoot:  "f8d90958c0db80812d728d2f441148db9e3205595fe84ac6dfb57ce05ee3452b",
		TxRoot:     "7faffb3de35b1e8d8267e59c8e469f6b462e3ea83debb2912d24a21d28e78fb3",
	}, parentRoots)
	tokenIDs := nonFinalPageTokens(t, env, seller, 4)
	sellerSeq := env.Seq(seller)
	createBatchBuilder := NewBatchBuilder(
		seller,
		sellerSeq,
		CalcBatchFeeFromEnv(env, 0, uint32(len(tokenIDs))),
		batchtx.BatchFlagAllOrNothing,
	)
	for i, tokenID := range tokenIDs {
		inner := nfttest.NFTokenCreateSellOffer(
			seller,
			tokenID,
			tx.NewXRPAmount(1),
		).Build()
		prepareNFTBatchInner(inner, sellerSeq+uint32(i)+1)
		createBatchBuilder.AddInnerTx(inner)
	}
	createBatch := createBatchBuilder.MustBuild()
	createResult := env.Submit(createBatch)
	jtx.RequireTxSuccess(t, createResult)
	env.Close()
	requireBatchLedgerData(t, env, createBatch, createResult,
		ter.TesSUCCESS, ter.TesSUCCESS, ter.TesSUCCESS, ter.TesSUCCESS)
	createEvidence := captureNFTBatchReplayEvidence(t, env, createBatch)
	t.Logf("create evidence: %+v", createEvidence)
	require.Equal(t, nftBatchReplayEvidence{
		LedgerHash: "b6f733b591572ff91ad6268170c6ab96c84a2335129f021d5ab52d40bca9b6d3",
		StateRoot:  "005bd6aaa6b90904d7daccdb3590edb2aaf8948855e7d0679006bd207eb2ac5f",
		TxRoot:     "b851d35c6fc2fdcc0b87c433b3e2faf937b73e9e4244776c614493db5ddcc2c5",
		TERResults: []string{"tesSUCCESS", "tesSUCCESS", "tesSUCCESS", "tesSUCCESS", "tesSUCCESS"},
		MetadataHash: []string{
			"649a463e62886e51bbe9ed30f1618a1a3cd4c2c46cb69ef5ba50a464dbdb94c0",
			"a35587e4db4494133bf31dd4c76961fdc284ea66867573c12fd67a4c98e0db1d",
			"6b24d2c7efb697c8f5cd13a4c7d184f4ebd83d8b2613d694fd54c83c47127ee8",
			"e19dba1f9c4ec27b8c46d365744e52e2ffb1e48ce501ecebedbbdf0e66ad2387",
			"c4d51ba858cd8e2493b92dc1ccf5a00b696351ac10ea0678b165fcc9dde9c9d9",
		},
	}, createEvidence)

	buyerSeq := env.Seq(buyer)
	acceptBatchBuilder := NewBatchBuilder(
		buyer,
		buyerSeq,
		CalcBatchFeeFromEnv(env, 0, uint32(len(tokenIDs))),
		batchtx.BatchFlagAllOrNothing,
	)
	for i := range tokenIDs {
		offerID := fmt.Sprintf("%X", keylet.NFTokenOffer(seller.ID, sellerSeq+uint32(i)+1).Key)
		inner := nfttest.NFTokenAcceptSellOffer(buyer, offerID).Build()
		prepareNFTBatchInner(inner, buyerSeq+uint32(i)+1)
		acceptBatchBuilder.AddInnerTx(inner)
	}
	acceptBatch := acceptBatchBuilder.MustBuild()
	acceptResult := env.Submit(acceptBatch)
	jtx.RequireTxSuccess(t, acceptResult)
	env.Close()
	requireBatchLedgerData(t, env, acceptBatch, acceptResult,
		ter.TesSUCCESS, ter.TesSUCCESS, ter.TesSUCCESS, ter.TesSUCCESS)
	acceptEvidence := captureNFTBatchReplayEvidence(t, env, acceptBatch)
	t.Logf("accept evidence: %+v", acceptEvidence)
	require.Equal(t, nftBatchReplayEvidence{
		LedgerHash: "699d899e73b49357dcc38ec88dc7091f41d6021142aa6fb37cc711f5c40f23e6",
		StateRoot:  "c66b47c2a097708c61bc4d6d7f2ab10104d0bb88063b02105ff15aa63f3d24de",
		TxRoot:     "956987f0437c741cbcd73da73bb69c1bba347c1b88777bd1462f0a1841c58922",
		TERResults: []string{"tesSUCCESS", "tesSUCCESS", "tesSUCCESS", "tesSUCCESS", "tesSUCCESS"},
		MetadataHash: []string{
			"682a72bacf614745546ed1eb51824bed6a3b71eb93a61b1c490d2420045359b8",
			"5c63fd6c023986ad9cc2d6b0e81550801a389dda471eaf893c404577eda05e81",
			"3e7d8002d3ae382f7b06135a05cc6bfd89229ca4baad2189b9fbd662040c7079",
			"710f9e487fd1e40674d5eb88ffd41657a305e48786a2875a23c910da8fb077da",
			"0eff564d1d2135f5d671375453308a88923d6aa7c72d9c4381ddffacb49e6673",
		},
	}, acceptEvidence)
}

func prepareNFTBatchInner(inner tx.Transaction, sequence uint32) {
	common := inner.GetCommon()
	common.Fee = "0"
	common.SigningPubKey = ""
	common.TxnSignature = ""
	common.SetSequence(sequence)
	common.SetFlags(common.GetFlags() | tx.TfInnerBatchTxn)
}

func nonFinalPageTokens(t *testing.T, env *jtx.TestEnv, owner *jtx.Account, count int) []string {
	t.Helper()
	maxPage := keylet.NFTokenPageMax(owner.ID)
	currentPage := maxPage
	var zero [32]byte
	var tokenIDs []string
	for {
		data, err := env.LedgerEntry(currentPage)
		require.NoError(t, err)
		page, err := state.ParseNFTokenPage(data)
		require.NoError(t, err)
		if currentPage.Key != maxPage.Key {
			for _, token := range page.NFTokens {
				tokenIDs = append(tokenIDs, hex.EncodeToString(token.NFTokenID[:]))
				if len(tokenIDs) == count {
					return tokenIDs
				}
			}
		}
		if page.PreviousPageMin == zero {
			break
		}
		currentPage = keylet.Keylet{Type: currentPage.Type, Key: page.PreviousPageMin}
	}
	require.Failf(t, "not enough non-final NFT page tokens", "got %d, want %d", len(tokenIDs), count)
	return nil
}

func captureNFTBatchReplayEvidence(t *testing.T, env *jtx.TestEnv, batch *batchtx.Batch) nftBatchReplayEvidence {
	t.Helper()
	roots := captureNFTBatchReplayRoots(t, env)
	closed := env.LastClosedLedger()
	evidence := nftBatchReplayEvidence{
		LedgerHash: roots.LedgerHash,
		StateRoot:  roots.StateRoot,
		TxRoot:     roots.TxRoot,
	}
	txns := make([]tx.Transaction, 0, 1+len(batch.RawTransactions))
	txns = append(txns, batch)
	for _, raw := range batch.RawTransactions {
		txns = append(txns, raw.RawTransaction.InnerTx)
	}
	for _, txn := range txns {
		txID, err := tx.ComputeTransactionHash(txn)
		require.NoError(t, err)
		stored, found, err := closed.GetTransaction(txID)
		require.NoError(t, err)
		require.True(t, found)
		_, metadata, err := tx.SplitTxWithMetaBlobStrict(stored)
		require.NoError(t, err)
		metadataHash := sha512half.Sum(metadata)
		evidence.MetadataHash = append(evidence.MetadataHash, hex.EncodeToString(metadataHash[:]))
		decoded, err := binarycodec.DecodeBytes(metadata)
		require.NoError(t, err)
		result, ok := decoded["TransactionResult"].(string)
		require.True(t, ok)
		evidence.TERResults = append(evidence.TERResults, result)
	}
	return evidence
}

func captureNFTBatchReplayRoots(t *testing.T, env *jtx.TestEnv) nftBatchReplayRoots {
	t.Helper()
	closed := env.LastClosedLedger()
	stateRoot, err := closed.StateMapHash()
	require.NoError(t, err)
	txRoot, err := closed.TxMapHash()
	require.NoError(t, err)
	ledgerHash := closed.Hash()
	return nftBatchReplayRoots{
		LedgerHash: hex.EncodeToString(ledgerHash[:]),
		StateRoot:  hex.EncodeToString(stateRoot[:]),
		TxRoot:     hex.EncodeToString(txRoot[:]),
	}
}
