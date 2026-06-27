// Package common holds helpers shared by the consensus vote producers
// (feevote, amendmentvote, negativeunlvote) that assemble pseudo-txs
// for injection into the flag-ledger tx set.
package common

import (
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/protocol"
)

// BuildPseudoTx serializes a consensus pseudo-tx: it stamps a zero-account
// BaseTx of txType, passes it to build to populate type-specific fields,
// then EncodePseudoTx fills the pseudo-tx defaults (zero fee/sequence,
// empty signing key).
func BuildPseudoTx(txType tx.Type, build func(base tx.BaseTx) tx.Transaction) ([]byte, error) {
	return pseudo.EncodePseudoTx(build(*tx.NewBaseTx(txType, protocol.ZeroAccount)))
}
