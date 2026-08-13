package engine

import (
	"context"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
)

// BlockProcessor handles batch application of transactions to a ledger.
// It wraps the Engine to provide higher-level functionality:
// - Applying multiple transactions in sequence
// - Assigning transaction indices based on processing order
// - Creating tx+meta blobs for the transaction tree
//
// This follows the rippled architecture where transactions are indexed
// by their processing order (not sorted by hash).
type BlockProcessor struct {
	// engine is the transaction engine
	engine *Engine

	createTxWithMetaBlob func([]byte, *txcore.Metadata) ([]byte, error)
}

// BlockTxResult contains the result of applying a single transaction in a block
type BlockTxResult struct {
	// Index is the transaction index in the block (0-based)
	Index uint32

	// Hash is the transaction hash
	Hash [32]byte

	// ApplyResult contains the engine's result
	ApplyResult txcore.ApplyResult

	// TxWithMetaBlob is the combined VL-encoded tx + VL-encoded metadata
	// This is what gets added to the transaction tree
	TxWithMetaBlob []byte

	// RawTxBlob is the original transaction blob
	RawTxBlob []byte
}

// NewBlockProcessor creates a new BlockProcessor with the given engine
func NewBlockProcessor(engine *Engine) *BlockProcessor {
	return &BlockProcessor{
		engine:               engine,
		createTxWithMetaBlob: txcore.CreateTxWithMetaBlob,
	}
}

// ApplyTransaction applies a single transaction and returns the result.
// It handles:
// - Calling the engine to apply the transaction
// - Creating the tx+meta blob
// - Publishing applied state and the transaction leaf atomically
// The engine assigns TransactionIndex in metadata for applied transactions.
func (bp *BlockProcessor) ApplyTransaction(transaction txcore.Transaction, txBlob []byte) (result BlockTxResult, err error) {
	return bp.applyTransaction(transaction, txBlob, false)
}

// ApplyLedgerTransaction applies a transaction while constructing a closed
// ledger, including any committed Batch inner transactions.
func (bp *BlockProcessor) ApplyLedgerTransaction(transaction txcore.Transaction, txBlob []byte) (result BlockTxResult, err error) {
	return bp.applyTransaction(transaction, txBlob, true)
}

func (bp *BlockProcessor) applyTransaction(
	transaction txcore.Transaction,
	txBlob []byte,
	applyBatchInners bool,
) (result BlockTxResult, err error) {
	transactionIndex := bp.engine.TxCount()
	// Backstop for the consensus build loop: any panic escaping the engine's
	// Apply-scoped recover — engine bookkeeping outside invokeApply, or the
	// pseudo-tx apply path which runs outside it — is converted to an error so
	// the caller (ApplyTxs / applyAndClassify) drops this one transaction and
	// keeps building the ledger. Mirrors rippled applyTransactions'
	// per-transaction catch(std::exception) that marks the tx failed and
	// continues (BuildLedger.cpp), rather than letting the throw terminate the
	// consensus goroutine.
	defer func() {
		if r := recover(); r != nil {
			bp.engine.logger.Error("transaction apply panic recovered, dropping tx",
				"panic", r)
			result = BlockTxResult{Index: transactionIndex, RawTxBlob: txBlob}
			err = fmt.Errorf("apply panic: %v", r)
		}
	}()
	if canonical := transaction.GetRawBytes(); len(canonical) != 0 {
		txBlob = canonical
	} else {
		canonical, err := txcore.SerializeTransaction(transaction)
		if err != nil {
			return result, err
		}
		txBlob = canonical
	}

	result = BlockTxResult{
		Index:     transactionIndex,
		RawTxBlob: txBlob,
	}

	// Compute transaction hash
	hash, err := txcore.ComputeTransactionHash(transaction)
	if err != nil {
		return result, err
	}
	result.Hash = hash

	base, ok := bp.engine.view.(*ledger.Ledger)
	if !ok {
		return result, fmt.Errorf("block processor requires a ledger-backed engine view")
	}

	staged, err := base.MutableSnapshotUnflushed()
	if err != nil {
		return result, fmt.Errorf("snapshot ledger for transaction apply: %w", err)
	}
	stagedEngine := NewEngine(staged, bp.engine.config)
	stagedEngine.SetBaseTxCount(bp.engine.TxCount())
	stagedEngine.invariantViolationHook = bp.engine.invariantViolationHook

	// Pseudo-transactions (Amendment, SetFee, UNLModify) use ApplyPseudo()
	// since Apply() rejects them (matching rippled's passesLocalChecks).
	var applyResult txcore.ApplyResult
	if transaction.TxType().IsPseudoTransaction() {
		applyResult = stagedEngine.ApplyPseudo(transaction)
	} else {
		applyResult = stagedEngine.Apply(transaction)
		if applyBatchInners {
			applyResult = stagedEngine.ApplyBatchInnerTransactions(context.Background(), transaction, applyResult)
		}
	}
	result.ApplyResult = applyResult

	if applyResult.Applied {
		// The engine assigns TransactionIndex in metadata for applied transactions
		// (matching rippled's txCount-based indexing), so we don't overwrite it here.
		txWithMetaBlob, err := bp.createTxWithMetaBlob(txBlob, applyResult.Metadata)
		if err != nil {
			return result, err
		}
		result.TxWithMetaBlob = txWithMetaBlob
		if err := staged.AddTransactionWithMeta(hash, txWithMetaBlob); err != nil {
			return result, fmt.Errorf("stage transaction metadata: %w", err)
		}
		for _, inner := range applyResult.AppliedInnerTransactions {
			if inner.Transaction == nil || inner.Metadata == nil {
				return result, fmt.Errorf("stage batch inner transaction: missing transaction or metadata")
			}
			innerBlob, err := txcore.SerializeTransaction(inner.Transaction)
			if err != nil {
				return result, fmt.Errorf("serialize batch inner transaction: %w", err)
			}
			innerHash, err := txcore.ComputeTransactionHash(inner.Transaction)
			if err != nil {
				return result, fmt.Errorf("hash batch inner transaction: %w", err)
			}
			innerWithMeta, err := bp.createTxWithMetaBlob(innerBlob, inner.Metadata)
			if err != nil {
				return result, fmt.Errorf("serialize batch inner metadata: %w", err)
			}
			if err := staged.AddTransactionWithMeta(innerHash, innerWithMeta); err != nil {
				return result, fmt.Errorf("stage batch inner metadata: %w", err)
			}
		}
		if err := base.AdoptState(staged); err != nil {
			return result, fmt.Errorf("commit transaction state and metadata: %w", err)
		}
		bp.engine.SetBaseTxCount(stagedEngine.TxCount())
	}

	return result, nil
}

// ParsedTx holds a parsed transaction along with its raw blob.
type ParsedTx struct {
	// Transaction is the parsed transaction
	Transaction txcore.Transaction

	RawBlob []byte
}

// ParseAndPrepare parses a transaction blob and returns a ParsedTx ready for processing.
// The returned transaction and blob retain the canonical field order.
func ParseAndPrepare(txBlob []byte) (*ParsedTx, error) {
	transaction, err := txcore.ParseFromBinary(txBlob)
	if err != nil {
		return nil, err
	}

	return &ParsedTx{
		Transaction: transaction,
		RawBlob:     transaction.GetRawBytes(),
	}, nil
}
