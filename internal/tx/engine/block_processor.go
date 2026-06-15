package engine

import txcore "github.com/LeJamon/go-xrpl/internal/tx"

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

	// txIndex tracks the current transaction index (0-based)
	txIndex uint32
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
		engine:  engine,
		txIndex: 0,
	}
}

// ApplyTransaction applies a single transaction and returns the result.
// It handles:
// - Calling the engine to apply the transaction
// - Creating the tx+meta blob
// The engine assigns TransactionIndex in metadata for applied transactions.
func (bp *BlockProcessor) ApplyTransaction(transaction txcore.Transaction, txBlob []byte) (BlockTxResult, error) {
	result := BlockTxResult{
		Index:     bp.txIndex,
		RawTxBlob: txBlob,
	}

	// Compute transaction hash
	hash, err := txcore.ComputeTransactionHash(transaction)
	if err != nil {
		return result, err
	}
	result.Hash = hash

	// Apply the transaction using the engine.
	// Pseudo-transactions (Amendment, SetFee, UNLModify) use ApplyPseudo()
	// since Apply() rejects them (matching rippled's passesLocalChecks).
	var applyResult txcore.ApplyResult
	if transaction.TxType().IsPseudoTransaction() {
		applyResult = bp.engine.ApplyPseudo(transaction)
	} else {
		applyResult = bp.engine.Apply(transaction)
	}
	result.ApplyResult = applyResult

	// Create the tx+meta blob for the transaction tree.
	// The engine assigns TransactionIndex in metadata for applied transactions
	// (matching rippled's txCount-based indexing), so we don't overwrite it here.
	txWithMetaBlob, err := txcore.CreateTxWithMetaBlob(txBlob, applyResult.Metadata)
	if err != nil {
		return result, err
	}
	result.TxWithMetaBlob = txWithMetaBlob

	// Increment the processing order counter for the next transaction
	bp.txIndex++

	return result, nil
}

// ParsedTx holds a parsed transaction along with its raw blob.
type ParsedTx struct {
	// Transaction is the parsed transaction
	Transaction txcore.Transaction

	// RawBlob is the original binary blob
	RawBlob []byte
}

// ParseAndPrepare parses a transaction blob and returns a ParsedTx ready for processing.
// It also sets the raw bytes on the transaction for hash computation.
func ParseAndPrepare(txBlob []byte) (*ParsedTx, error) {
	transaction, err := txcore.ParseFromBinary(txBlob)
	if err != nil {
		return nil, err
	}

	// Store the raw bytes for hash computation
	transaction.SetRawBytes(txBlob)

	return &ParsedTx{
		Transaction: transaction,
		RawBlob:     txBlob,
	}, nil
}
