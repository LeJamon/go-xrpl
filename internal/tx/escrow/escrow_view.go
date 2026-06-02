package escrow

import (
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/nftoken"
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

// FindNFTURI returns the URI of the NFToken held by account, walking its
// NFTokenPages. It backs the get_nft host function a finish function calls.
func (v *escrowView) FindNFTURI(account [20]byte, nftID [32]byte) ([]byte, bool) {
	return nftoken.FindTokenURI(v.ctx.View, account, nftID)
}
