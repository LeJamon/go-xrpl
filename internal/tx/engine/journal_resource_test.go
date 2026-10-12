package engine

import (
	"bytes"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

type oversizedJournalTx struct {
	*txcore.BaseTx
}

func (oversizedJournalTx) Apply(ctx *txcore.ApplyContext) ter.Result {
	// The bounded view must preserve this local admission failure as an
	// operational Cause even though the handler itself returns a protocol result.
	_ = ctx.View.Insert(keylet.Keylet{Key: [32]byte{0x99}}, bytes.Repeat([]byte{0xa5}, 32<<10))
	return ter.TesSUCCESS
}

func TestBoundedEnginePreservesJournalResourceCause(t *testing.T) {
	view := newRecordingBaseView()
	view.cfg = overlay.Config{MaxMemoryBytes: 8 << 10}
	accountKey := fundRecoveryAccount(t, view, 1_000_000, 1)
	result := recoveryEngine(view, txcore.TapNONE).Apply(oversizedJournalTx{BaseTx: recoveryTx(10, 1)})
	if result.Result != ter.TefEXCEPTION || result.Applied {
		t.Fatalf("result=(%s,%t), want tefEXCEPTION and not applied", result.Result, result.Applied)
	}
	if !errors.Is(result.Cause, overlay.ErrSpillDisabled) {
		t.Fatalf("cause=%v, want ErrSpillDisabled", result.Cause)
	}
	account := readRecoveryAccount(t, view, accountKey)
	if account.Balance != 1_000_000 || account.Sequence != 1 {
		t.Fatalf("account balance/sequence=%d/%d, want 1000000/1", account.Balance, account.Sequence)
	}
}
