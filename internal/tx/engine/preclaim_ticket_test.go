package engine

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

type ticketExistsView struct {
	*mockBaseView
	exists bool
	err    error
	calls  int
	key    keylet.Keylet
}

func (v *ticketExistsView) Exists(k keylet.Keylet) (bool, error) {
	v.calls++
	v.key = k
	return v.exists, v.err
}

func TestCheckSeqProxyTicketExists(t *testing.T) {
	lookupErr := errors.New("ticket lookup failed")
	tests := []struct {
		name           string
		ticketSequence uint32
		exists         bool
		err            error
		want           ter.Result
		wantCalls      int
	}{
		{name: "future ticket", ticketSequence: 10, err: lookupErr, want: ter.TerPRE_TICKET},
		{name: "missing ticket", ticketSequence: 9, want: ter.TefNO_TICKET, wantCalls: 1},
		{name: "present ticket", ticketSequence: 9, exists: true, want: ter.TesSUCCESS, wantCalls: 1},
		{name: "lookup error", ticketSequence: 9, err: lookupErr, want: ter.TefINTERNAL, wantCalls: 1},
		{name: "lookup error with present result", ticketSequence: 9, exists: true, err: lookupErr, want: ter.TefINTERNAL, wantCalls: 1},
	}

	accountID := [20]byte{1}
	account := &state.AccountRoot{Sequence: 10}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			view := &ticketExistsView{
				mockBaseView: newMockBaseView(),
				exists:       test.exists,
				err:          test.err,
			}
			engine := NewEngine(view, txcore.EngineConfig{})
			common := &txcore.Common{TicketSequence: &test.ticketSequence}

			if got := engine.checkSeqProxy(common, accountID, account); got != test.want {
				t.Fatalf("checkSeqProxy() = %v, want %v", got, test.want)
			}
			if view.calls != test.wantCalls {
				t.Fatalf("Exists calls = %d, want %d", view.calls, test.wantCalls)
			}
			if test.wantCalls > 0 {
				wantKey := keylet.Ticket(accountID, test.ticketSequence)
				if view.key != wantKey {
					t.Fatalf("Exists key = %v, want %v", view.key, wantKey)
				}
			}
		})
	}
}
