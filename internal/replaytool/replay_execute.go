package replaytool

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/statecompare"
	"github.com/LeJamon/go-xrpl/internal/tx"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

type blockTransaction struct {
	Index       int
	Hash        [32]byte
	Blob        []byte
	Transaction tx.Transaction
}

type blockExecution struct {
	StateMap                             *shamap.SHAMap
	LedgerIndex                          uint32
	ParentHash                           [32]byte
	ParentCloseTime                      time.Time
	CloseTime                            time.Time
	CloseTimeResolution                  uint8
	CloseFlags                           uint8
	TotalCoins                           uint64
	Fees                                 drops.Fees
	Rules                                *amendment.Rules
	Transactions                         []blockTransaction
	ReplayPreFixPayChanRecipientOwnerDir bool
	WantTxDetail                         bool
	WantPostStateCount                   bool
}

type executedBlock struct {
	Ledger          *ledger.Ledger
	StateMap        *shamap.SHAMap
	LedgerHash      [32]byte
	AccountHash     [32]byte
	TransactionHash [32]byte
	TotalCoins      uint64
	PostStateCount  int
	TxResults       []txApplyInfo
	Errors          []string
}

func executeBlock(ctx context.Context, input blockExecution) (*executedBlock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if input.StateMap == nil {
		return nil, fmt.Errorf("block state map is nil")
	}
	if input.Rules == nil {
		return nil, fmt.Errorf("block amendment rules are nil")
	}

	applicationCloseTime := ledger.ApplicationViewCloseTime(
		input.ParentCloseTime,
		input.CloseTime,
		uint32(input.CloseTimeResolution),
	)
	ledgerHeader := header.LedgerHeader{
		LedgerIndex:         input.LedgerIndex,
		ParentHash:          input.ParentHash,
		ParentCloseTime:     input.ParentCloseTime,
		CloseTime:           applicationCloseTime,
		CloseTimeResolution: input.CloseTimeResolution,
		CloseFlags:          input.CloseFlags,
		Drops:               input.TotalCoins,
	}
	openLedger, err := ledger.NewOpenWithHeader(
		ledgerHeader,
		input.StateMap,
		shamap.New(shamap.TypeTransaction),
		input.Fees,
	)
	if err != nil {
		return nil, fmt.Errorf("creating replay ledger: %w", err)
	}
	if protocol.IsFlagLedger(input.LedgerIndex) {
		if err := openLedger.UpdateNegativeUNL(); err != nil {
			return nil, fmt.Errorf("flag-ledger updateNegativeUNL: %w", err)
		}
	}

	engine := txengine.NewEngine(openLedger, tx.EngineConfig{
		BaseFee:                              uint64(input.Fees.Base),
		ReserveBase:                          uint64(input.Fees.Reserve),
		ReserveIncrement:                     uint64(input.Fees.Increment),
		LedgerSequence:                       input.LedgerIndex,
		ParentHash:                           input.ParentHash,
		ParentCloseTime:                      protocol.ToRippleTime(input.ParentCloseTime),
		ApplicationCloseTime:                 protocol.ToRippleTime(applicationCloseTime),
		ApplicationCloseTimeSet:              true,
		SkipSignatureVerification:            true,
		Standalone:                           true,
		ReplayPreFixPayChanRecipientOwnerDir: input.ReplayPreFixPayChanRecipientOwnerDir,
		Rules:                                input.Rules,
	})
	processor := txengine.NewBlockProcessor(engine)
	result := &executedBlock{
		Ledger:    openLedger,
		TxResults: make([]txApplyInfo, 0, len(input.Transactions)),
		Errors:    make([]string, 0),
	}
	for _, transaction := range input.Transactions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		txInfo := txApplyInfo{
			Index: transaction.Index,
			Hash:  hex.EncodeToString(transaction.Hash[:]),
		}
		fillTxDisplay(&txInfo, transaction.Blob, transaction.Transaction, input.WantTxDetail)
		blockTxResult, err := processor.ApplyLedgerTransaction(transaction.Transaction, transaction.Blob)
		if err != nil {
			return nil, fmt.Errorf("tx %d apply: %w", transaction.Index, err)
		}
		if blockTxResult.Hash != transaction.Hash {
			return nil, fmt.Errorf("tx %d processor hash %x does not match validated hash %x", transaction.Index, blockTxResult.Hash, transaction.Hash)
		}
		applyResult := blockTxResult.ApplyResult
		txInfo.Result = applyResult.Result.String()
		txInfo.ResultCode = int(applyResult.Result)
		txInfo.Applied = applyResult.Applied
		txInfo.Fee = applyResult.Fee
		if applyResult.Applied {
			_, metadata, err := tx.SplitTxWithMetaBlobStrict(blockTxResult.TxWithMetaBlob)
			if err != nil {
				return nil, fmt.Errorf("tx %d extracting applied metadata: %w", transaction.Index, err)
			}
			txInfo.MetaBlob = metadata
		}
		result.TxResults = append(result.TxResults, txInfo)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := openLedger.Close(input.CloseTime, input.CloseFlags); err != nil {
		return nil, fmt.Errorf("closing ledger: %w", err)
	}
	result.LedgerHash = openLedger.Hash()
	result.AccountHash, err = openLedger.StateMapHash()
	if err != nil {
		return nil, fmt.Errorf("computing state map hash: %w", err)
	}
	result.TransactionHash, err = openLedger.TxMapHash()
	if err != nil {
		return nil, fmt.Errorf("computing transaction map hash: %w", err)
	}
	result.TotalCoins = openLedger.TotalDrops()
	result.StateMap, err = openLedger.StateMapSnapshot()
	if err != nil {
		return nil, fmt.Errorf("getting state snapshot: %w", err)
	}
	if input.WantPostStateCount {
		if err := result.StateMap.ForEachCtxReleasing(ctx, func(*shamap.Item) bool {
			result.PostStateCount++
			return true
		}); err != nil {
			return nil, fmt.Errorf("counting post-state entries: %w", err)
		}
	}
	return result, nil
}

func (r *replayRangeRunner) processBlockShared(
	ctx context.Context,
	client *statecompare.Client,
	preStateMap *shamap.SHAMap,
	preSnapshot *statecompare.LedgerSnapshot,
	targetLedger uint32,
	fees drops.Fees,
) (*blockResult, *shamap.SHAMap, error) {
	postSnapshot, err := client.Snapshot(ctx, targetLedger)
	if err != nil {
		return nil, nil, fmt.Errorf("getting target snapshot: %w", err)
	}
	if err := validateReplaySnapshotLink(preSnapshot, postSnapshot, targetLedger); err != nil {
		return nil, nil, err
	}
	closeTime, err := replayCloseTime(postSnapshot.CloseTime)
	if err != nil {
		return nil, nil, fmt.Errorf("ledger %d close time: %w", targetLedger, err)
	}
	parentCloseTime, err := replayCloseTime(preSnapshot.CloseTime)
	if err != nil {
		return nil, nil, fmt.Errorf("ledger %d parent close time: %w", targetLedger, err)
	}
	resolution := consensus.GetNextLedgerTimeResolution(
		preSnapshot.CloseTimeResolution,
		preSnapshot.CloseFlags&header.LCFNoConsensusTime == 0,
		targetLedger,
	)
	if resolution != postSnapshot.CloseTimeResolution {
		return nil, nil, fmt.Errorf(
			"ledger %d close time resolution: got %d, derived %d from parent",
			targetLedger, postSnapshot.CloseTimeResolution, resolution,
		)
	}
	if resolution > 255 {
		return nil, nil, fmt.Errorf("ledger %d close time resolution %d exceeds uint8", targetLedger, resolution)
	}
	rules, err := loadRulesFromState(preStateMap)
	if err != nil {
		return nil, nil, fmt.Errorf("loading amendments: %w", err)
	}
	transactions, err := client.Transactions(ctx, postSnapshot)
	if err != nil {
		return nil, nil, fmt.Errorf("getting transactions: %w", err)
	}
	prepared := make([]blockTransaction, len(transactions))
	for i, transaction := range transactions {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if transaction.TxIndex != uint32(i) {
			return nil, nil, fmt.Errorf("ledger %d transaction indices are not contiguous: position %d has %d", targetLedger, i, transaction.TxIndex)
		}
		parsed, err := txengine.ParseAndPrepare(transaction.TxBlob)
		if err != nil {
			return nil, nil, fmt.Errorf("ledger %d tx %d parse: %w", targetLedger, i, err)
		}
		hash, err := tx.ComputeTransactionHash(parsed.Transaction)
		if err != nil {
			return nil, nil, fmt.Errorf("ledger %d tx %d hash: %w", targetLedger, i, err)
		}
		if hash != transaction.TxHash {
			return nil, nil, fmt.Errorf("ledger %d tx %d hash %x does not match blob hash %x", targetLedger, i, transaction.TxHash, hash)
		}
		prepared[i] = blockTransaction{
			Index:       i,
			Hash:        hash,
			Blob:        transaction.TxBlob,
			Transaction: parsed.Transaction,
		}
	}

	executed, err := executeBlock(ctx, blockExecution{
		StateMap:                             preStateMap,
		LedgerIndex:                          targetLedger,
		ParentHash:                           preSnapshot.LedgerHash,
		ParentCloseTime:                      parentCloseTime,
		CloseTime:                            closeTime,
		CloseTimeResolution:                  uint8(resolution),
		CloseFlags:                           postSnapshot.CloseFlags,
		TotalCoins:                           preSnapshot.TotalCoins,
		Fees:                                 fees,
		Rules:                                rules,
		Transactions:                         prepared,
		ReplayPreFixPayChanRecipientOwnerDir: replayPreFixPayChanRecipientOwnerDir(targetLedger, r.legacyPayChanDirGate, r.payChanDirFirstFixed),
		WantTxDetail:                         r.decoded,
	})
	if err != nil {
		return nil, nil, err
	}
	result := &blockResult{
		TxCount:                 len(transactions),
		LedgerHash:              executed.LedgerHash,
		AccountHash:             executed.AccountHash,
		TransactionHash:         executed.TransactionHash,
		TotalCoins:              executed.TotalCoins,
		ExpectedLedgerHash:      postSnapshot.LedgerHash,
		ExpectedAccountHash:     postSnapshot.AccountHash,
		ExpectedTransactionHash: postSnapshot.TransactionHash,
		ExpectedTotalCoins:      postSnapshot.TotalCoins,
		PostSnapshot:            postSnapshot,
		TxResults:               executed.TxResults,
		Errors:                  executed.Errors,
		Rules:                   rules,
	}
	for i := range transactions {
		if !bytes.Equal(result.TxResults[i].MetaBlob, transactions[i].MetaBlob) {
			result.Errors = append(result.Errors, fmt.Sprintf("tx %d metadata does not match captured ledger", i))
		}
		if r.decoded {
			encoded, err := json.Marshal(result.TxResults[i].DecodedTx)
			if err != nil {
				return nil, nil, fmt.Errorf("encoding decoded tx %d: %w", i, err)
			}
			fmt.Fprintf(r.out, "        [%d] %s\n", i, encoded)
		}
	}
	result.Success = result.LedgerHash == result.ExpectedLedgerHash &&
		result.AccountHash == result.ExpectedAccountHash &&
		result.TransactionHash == result.ExpectedTransactionHash &&
		result.TotalCoins == result.ExpectedTotalCoins &&
		len(result.Errors) == 0
	return result, executed.StateMap, nil
}
