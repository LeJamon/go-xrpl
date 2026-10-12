package txq

import (
	"bytes"
	"fmt"
	"slices"

	"github.com/LeJamon/go-xrpl/internal/tx"
)

// Fork returns an independent queue snapshot suitable for private preparation.
// The fork owns every mutable queue container and candidate state. Canonical
// transaction blobs are parsed again so the fork does not share parsed
// transaction objects with the source queue.
func (q *TxQ) Fork() (*TxQ, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.stateMu.RLock()
	defer q.stateMu.RUnlock()

	return forkTxQ(q)
}

func forkTxQ(q *TxQ) (*TxQ, error) {
	fork := &TxQ{
		config:     q.config,
		feeMetrics: forkFeeMetrics(q.feeMetrics),
		parentHash: q.parentHash,
		txqFull:    q.txqFull,
	}

	if q.maxSize != nil {
		maxSize := *q.maxSize
		fork.maxSize = &maxSize
	}

	cloner := txQForkCloner{
		candidates: make(map[*candidate]*candidate, len(q.byFee)),
	}

	if q.byAccount != nil {
		fork.byAccount = make(map[[20]byte]*accountQueue, len(q.byAccount))
		for account, source := range q.byAccount {
			if source == nil {
				fork.byAccount[account] = nil
				continue
			}
			cloned := &accountQueue{
				Account:      source.Account,
				RetryPenalty: source.RetryPenalty,
				DropPenalty:  source.DropPenalty,
			}
			if source.Transactions != nil {
				cloned.Transactions = make(map[SeqProxy]*candidate, len(source.Transactions))
				for seqProxy, sourceCandidate := range source.Transactions {
					candidate, err := cloner.candidate(sourceCandidate)
					if err != nil {
						return nil, err
					}
					cloned.Transactions[seqProxy] = candidate
				}
			}
			fork.byAccount[account] = cloned
		}
	}

	if q.byFee != nil {
		fork.byFee = make([]*candidate, len(q.byFee))
		for i, candidate := range q.byFee {
			cloned, err := cloner.candidate(candidate)
			if err != nil {
				return nil, err
			}
			fork.byFee[i] = cloned
		}
	}

	if q.byID != nil {
		fork.byID = make(map[[32]byte]*candidate, len(q.byID))
		for txID, candidate := range q.byID {
			cloned, err := cloner.candidate(candidate)
			if err != nil {
				return nil, err
			}
			fork.byID[txID] = cloned
		}
	}

	return fork, nil
}

type txQForkCloner struct {
	candidates map[*candidate]*candidate
}

func (c *txQForkCloner) candidate(source *candidate) (*candidate, error) {
	if source == nil {
		return nil, nil
	}
	if cloned, ok := c.candidates[source]; ok {
		return cloned, nil
	}

	cloned := *source
	cloned.blob = bytes.Clone(source.blob)
	c.candidates[source] = &cloned

	if len(cloned.blob) > 0 {
		parsed, err := tx.ParseFromBinary(cloned.blob)
		if err != nil {
			return nil, fmt.Errorf("transaction queue: fork candidate %x: parse blob: %w", source.TxID, err)
		}
		cloned.Txn = parsed
	} else if source.Txn != nil {
		serialized, err := tx.SerializeTransaction(source.Txn)
		if err != nil {
			return nil, fmt.Errorf("transaction queue: fork candidate %x: serialize transaction: %w", source.TxID, err)
		}
		cloned.blob = bytes.Clone(serialized)
		parsed, err := tx.ParseFromBinary(cloned.blob)
		if err != nil {
			return nil, fmt.Errorf("transaction queue: fork candidate %x: parse serialized transaction: %w", source.TxID, err)
		}
		cloned.Txn = parsed
	}

	return &cloned, nil
}

func forkFeeMetrics(source *feeMetrics) *feeMetrics {
	if source == nil {
		return nil
	}
	cloned := *source
	cloned.recentTxnCounts = slices.Clone(source.recentTxnCounts)
	return &cloned
}
