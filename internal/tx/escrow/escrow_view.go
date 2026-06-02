package escrow

import (
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/wasm/host"
	"github.com/LeJamon/go-xrpl/keylet"
)

// escrowView adapts an ApplyContext and the escrow being finished to the
// host.View interface the WASM finish function reads ledger state through.
type escrowView struct {
	ctx         *tx.ApplyContext
	txBytes     []byte // the EscrowFinish transaction, serialized
	escrowBytes []byte // the escrow ledger object, serialized (the current object)
}

var _ host.View = (*escrowView)(nil)

func (v *escrowView) LedgerSeq() uint32                 { return v.ctx.Config.LedgerSequence }
func (v *escrowView) ParentCloseTime() uint32           { return v.ctx.Config.ParentCloseTime }
func (v *escrowView) ParentHash() [32]byte              { return v.ctx.Config.ParentHash }
func (v *escrowView) BaseFee() uint32                   { return uint32(v.ctx.Config.BaseFee) }
func (v *escrowView) AmendmentEnabled(id [32]byte) bool { return v.ctx.Rules().Enabled(id) }
func (v *escrowView) TxBytes() []byte                   { return v.txBytes }
func (v *escrowView) CurrentObjBytes() []byte           { return v.escrowBytes }

func (v *escrowView) ReadSLE(index [32]byte) ([]byte, bool) {
	data, err := v.ctx.View.Read(keylet.Keylet{Key: index})
	if err != nil || data == nil {
		return nil, false
	}
	return data, true
}

// FindNFTURI is not yet wired (NFTokenPage traversal); get_nft from a finish
// function returns not-found for now.
func (v *escrowView) FindNFTURI(_ [20]byte, _ [32]byte) ([]byte, bool) {
	return nil, false
}
