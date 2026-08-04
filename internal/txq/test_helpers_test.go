package txq

import (
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func mustNew(cfg Config) *TxQ {
	q, err := New(cfg)
	if err != nil {
		panic(err)
	}
	return q
}

// These aliases keep legacy same-package fixtures readable without exporting
// the queue's traversal types from production code.
type Candidate = candidate
type TxConsequences = txConsequences
type AccountQueue = accountQueue
type FeeMetrics = feeMetrics
type Snapshot = feeMetricsSnapshot

func NewFeeMetrics(cfg Config) *feeMetrics          { return newFeeMetrics(cfg) }
func (fm *feeMetrics) Snapshot() feeMetricsSnapshot { return fm.snapshot() }
func (fm *feeMetrics) Update(feeLevels []FeeLevel, timeLeap bool, cfg Config) uint64 {
	return fm.update(feeLevels, timeLeap, cfg)
}
func ScaleFeeLevel(snapshot feeMetricsSnapshot, txInLedger uint32) FeeLevel {
	return scaleFeeLevel(snapshot, txInLedger)
}
func EscalatedSeriesFeeLevel(snapshot feeMetricsSnapshot, txInLedger, extraCount, seriesSize uint32) (FeeLevel, bool) {
	return escalatedSeriesFeeLevel(snapshot, txInLedger, extraCount, seriesSize)
}

func NewAccountQueue(account [20]byte) *accountQueue { return newAccountQueue(account) }

func NewCandidate(
	txn tx.Transaction,
	txID [32]byte,
	account [20]byte,
	feeLevel FeeLevel,
	seqProxy SeqProxy,
	lastValid uint32,
	preflightResult ter.Result,
	consequences txConsequences,
) *candidate {
	c := newCandidate(txID, account, feeLevel, seqProxy, lastValid, preflightResult, consequences)
	c.Txn = txn
	return c
}
