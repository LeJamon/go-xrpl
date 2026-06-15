package tx

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// DirRemoveOrBadLedger removes an item from a directory page, returning
// tefBAD_LEDGER if the page or item could not be found. rippled treats a failed
// dirRemove during object teardown (escrow finish/cancel, channel close, check
// cash) as a corrupt-ledger condition. keepRoot is passed through to
// state.DirRemove; the teardown call sites use keepRoot=true.
func DirRemoveOrBadLedger(view LedgerView, dir keylet.Keylet, page uint64, item [32]byte) ter.Result {
	res, err := state.DirRemove(view, dir, page, item, true)
	if err != nil || res == nil || !res.Success {
		return ter.TefBAD_LEDGER
	}
	return ter.TesSUCCESS
}
