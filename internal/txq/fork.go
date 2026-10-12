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
		candidates:       make(map[*candidate]*candidate, len(q.byFee)),
		candidateErrs:    make(map[*candidate]error),
		accountQueues:    make(map[*accountQueue]*accountQueue, len(q.byAccount)),
		accountQueueErrs: make(map[*accountQueue]error),
	}

	if q.byAccount != nil {
		fork.byAccount = make(map[[20]byte]*accountQueue, len(q.byAccount))
		for account, queue := range q.byAccount {
			cloned, err := cloner.accountQueue(queue)
			if err != nil {
				return nil, err
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
	candidates       map[*candidate]*candidate
	candidateErrs    map[*candidate]error
	accountQueues    map[*accountQueue]*accountQueue
	accountQueueErrs map[*accountQueue]error
}

func (c *txQForkCloner) candidate(source *candidate) (*candidate, error) {
	if source == nil {
		return nil, nil
	}
	if cloned, ok := c.candidates[source]; ok {
		return cloned, c.candidateErrs[source]
	}

	cloned := *source
	cloned.blob = bytes.Clone(source.blob)
	c.candidates[source] = &cloned

	if len(source.blob) > 0 {
		parsed, err := tx.ParseFromBinary(source.blob)
		if err != nil {
			// Synthetic transactions are private test fixtures that deliberately
			// do not have a wire representation. Production candidates always
			// retain a canonical blob and take the parsed branch above.
			if _, synthetic := source.Txn.(syntheticTransaction); !synthetic {
				wrapped := fmt.Errorf("transaction queue: fork candidate %x: clone transaction: %w", source.TxID, err)
				c.candidateErrs[source] = wrapped
				return nil, wrapped
			}
		} else {
			cloned.Txn = parsed
		}
	} else if source.Txn != nil {
		serialized, err := tx.SerializeTransaction(source.Txn)
		if err == nil {
			parsed, parseErr := tx.ParseFromBinary(serialized)
			if parseErr == nil {
				cloned.Txn = parsed
				cloned.blob = bytes.Clone(serialized)
			} else if _, synthetic := source.Txn.(syntheticTransaction); !synthetic {
				wrapped := fmt.Errorf("transaction queue: fork candidate %x: parse cloned transaction: %w", source.TxID, parseErr)
				c.candidateErrs[source] = wrapped
				return nil, wrapped
			}
		} else if _, synthetic := source.Txn.(syntheticTransaction); !synthetic {
			wrapped := fmt.Errorf("transaction queue: fork candidate %x: serialize transaction: %w", source.TxID, err)
			c.candidateErrs[source] = wrapped
			return nil, wrapped
		}
	}

	return &cloned, nil
}

func (c *txQForkCloner) accountQueue(source *accountQueue) (*accountQueue, error) {
	if source == nil {
		return nil, nil
	}
	if cloned, ok := c.accountQueues[source]; ok {
		return cloned, c.accountQueueErrs[source]
	}

	cloned := &accountQueue{
		Account:      source.Account,
		RetryPenalty: source.RetryPenalty,
		DropPenalty:  source.DropPenalty,
	}
	c.accountQueues[source] = cloned

	if source.Transactions != nil {
		cloned.Transactions = make(map[SeqProxy]*candidate, len(source.Transactions))
		for seqProxy, candidate := range source.Transactions {
			clonedCandidate, err := c.candidate(candidate)
			if err != nil {
				c.accountQueueErrs[source] = err
				return nil, err
			}
			cloned.Transactions[seqProxy] = clonedCandidate
		}
	}

	return cloned, nil
}

func forkFeeMetrics(source *feeMetrics) *feeMetrics {
	if source == nil {
		return nil
	}
	cloned := *source
	cloned.recentTxnCounts = slices.Clone(source.recentTxnCounts)
	return &cloned
}
