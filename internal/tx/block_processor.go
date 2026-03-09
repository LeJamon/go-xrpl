package tx

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
	ApplyResult ApplyResult

	// TxWithMetaBlob is the combined VL-encoded tx + VL-encoded metadata
	// This is what gets added to the transaction tree
	TxWithMetaBlob []byte

	// RawTxBlob is the original transaction blob
	RawTxBlob []byte
}

// BlockResult contains the results of applying all transactions in a block
type BlockResult struct {
	// Transactions contains results for each transaction
	Transactions []BlockTxResult

	// TotalFee is the sum of all fees charged
	TotalFee uint64

	// AppliedCount is the number of successfully applied transactions
	AppliedCount int

	// FailedCount is the number of failed transactions
	FailedCount int
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
// - Assigning the transaction index
// - Creating the tx+meta blob
func (bp *BlockProcessor) ApplyTransaction(transaction Transaction, txBlob []byte) (BlockTxResult, error) {
	result := BlockTxResult{
		Index:     bp.txIndex,
		RawTxBlob: txBlob,
	}

	// Compute transaction hash
	hash, err := computeTransactionHash(transaction)
	if err != nil {
		return result, err
	}
	result.Hash = hash

	// Apply the transaction using the engine
	applyResult := bp.engine.Apply(transaction)
	result.ApplyResult = applyResult

	// Set the transaction index in the metadata
	if applyResult.Metadata != nil {
		applyResult.Metadata.TransactionIndex = bp.txIndex
	}

	// Create the tx+meta blob for the transaction tree
	txWithMetaBlob, err := CreateTxWithMetaBlob(txBlob, applyResult.Metadata)
	if err != nil {
		return result, err
	}
	result.TxWithMetaBlob = txWithMetaBlob

	// Increment the transaction index for the next transaction
	bp.txIndex++

	return result, nil
}

// ApplyTransactions applies multiple transactions in sequence.
// Transactions are indexed based on the order they appear in the slice.
func (bp *BlockProcessor) ApplyTransactions(transactions []ParsedTx) (*BlockResult, error) {
	result := &BlockResult{
		Transactions: make([]BlockTxResult, 0, len(transactions)),
	}

	for _, ptx := range transactions {
		txResult, err := bp.ApplyTransaction(ptx.Transaction, ptx.RawBlob)
		if err != nil {
			// Log the error but continue with other transactions
			// The error is captured in the result
			txResult.ApplyResult.Message = "block processor error: " + err.Error()
		}

		result.Transactions = append(result.Transactions, txResult)
		result.TotalFee += txResult.ApplyResult.Fee

		if txResult.ApplyResult.Applied {
			result.AppliedCount++
		} else {
			result.FailedCount++
		}
	}

	return result, nil
}

// ResetIndex resets the transaction index counter.
// This is useful when starting a new block.
func (bp *BlockProcessor) ResetIndex() {
	bp.txIndex = 0
}

// CurrentIndex returns the current transaction index.
func (bp *BlockProcessor) CurrentIndex() uint32 {
	return bp.txIndex
}

// ParsedTx holds a parsed transaction along with its raw blob.
// This is used as input to ApplyTransactions.
type ParsedTx struct {
	// Transaction is the parsed transaction
	Transaction Transaction

	// RawBlob is the original binary blob
	RawBlob []byte
}

// ParseAndPrepare parses a transaction blob and returns a ParsedTx ready for processing.
// It also sets the raw bytes on the transaction for hash computation.
func ParseAndPrepare(txBlob []byte) (*ParsedTx, error) {
	transaction, err := ParseFromBinary(txBlob)
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
