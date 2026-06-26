// Package common holds helpers shared by the consensus vote producers
// (feevote, amendmentvote, negativeunlvote) that assemble pseudo-txs
// for injection into the flag-ledger tx set.
package common

import (
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/protocol"
)

// BuildPseudoTx assembles and serializes a consensus pseudo-tx. It
// stamps the canonical pseudo-tx envelope — zero account, the txType's
// BaseTx — then hands that BaseTx to build, which embeds it in the
// concrete pseudo-tx struct and populates the type-specific fields. The
// returned struct is serialized via pseudo.EncodePseudoTx, which fills
// the remaining rippled-default common fields (zero fee, zero sequence,
// empty signing key).
//
// This factors out the NewBaseTx(ZeroAccount)→EncodePseudoTx boilerplate
// the three vote producers otherwise repeat; only the tx type and the
// field population (the build closure) differ between them.
func BuildPseudoTx(txType tx.Type, build func(base tx.BaseTx) tx.Transaction) ([]byte, error) {
	return pseudo.EncodePseudoTx(build(*tx.NewBaseTx(txType, protocol.ZeroAccount)))
}
