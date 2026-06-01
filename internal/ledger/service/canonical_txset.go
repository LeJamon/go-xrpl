package service

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
)

type pendingTx = openledger.PendingTx

func canonicalSort(txs []pendingTx, salt [32]byte) { openledger.CanonicalSort(txs, salt) }

func parsePendingTx(blob []byte) (pendingTx, error) { return openledger.ParsePendingTx(blob) }

func computeSalt(txs []pendingTx) [32]byte { return openledger.ComputeSalt(txs) }
