package engine

import (
	"bytes"
	"encoding/binary"
	"math/big"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/applystate"
	"github.com/LeJamon/go-xrpl/internal/tx/invariants"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

const (
	xrpOverflowAccountCount = 185
	xrpOverflowFee          = 10
	// Keep this untyped: 2^64 is intentionally outside uint64's range. Each
	// individual AccountRoot delta below remains below InitialXRP.
	xrpOverflowTotalDelta = 1 << 64
)

type xrpOverflowTarget struct {
	key       keylet.Keylet
	before    []byte
	afterData []byte
}

// xrpOverflowTx injects synthetic balance changes into existing accounts;
// fee and sequence handling still run through the production engine.
type xrpOverflowTx struct {
	*txcore.BaseTx
	targets []xrpOverflowTarget
}

func (tx xrpOverflowTx) Apply(ctx *txcore.ApplyContext) ter.Result {
	for _, target := range tx.targets {
		if err := ctx.View.Update(target.key, target.afterData); err != nil {
			return ter.TefINTERNAL
		}
	}
	return ter.TesSUCCESS
}

func TestApplyXRPNotCreated_Uint64WrapRecovery(t *testing.T) {
	for _, test := range []struct {
		name   string
		ticket bool
	}{
		{name: "sequence", ticket: false},
		{name: "ticket", ticket: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			view := newRecordingBaseView()
			var sourceKey, ticketKey keylet.Keylet
			if test.ticket {
				sourceKey, ticketKey, _ = seedXRPOverflowTicketAccount(t, view)
			} else {
				sourceKey = fundRecoveryAccount(t, view, 1_000_000, 1)
			}
			targets := seedXRPOverflowTargets(t, view)
			txn := newXRPOverflowTx(targets, test.ticket)

			// No hook is installed: recovery must come from the real checker.
			result := recoveryEngine(view, txcore.TapNONE).Apply(txn)
			if result.Result != ter.TecINVARIANT_FAILED || !result.Applied {
				t.Fatalf("result/applied = %s/%v, want tecINVARIANT_FAILED/true", result.Result, result.Applied)
			}
			if result.Fee != xrpOverflowFee {
				t.Fatalf("charged fee = %d, want %d", result.Fee, xrpOverflowFee)
			}
			if view.destroyed != drops.XRPAmount(xrpOverflowFee) {
				t.Fatalf("destroyed drops = %d, want %d", view.destroyed, xrpOverflowFee)
			}
			if view.outsideAdjustments != 0 {
				t.Fatalf("destroyed drops adjusted outside atomic commit %d times", view.outsideAdjustments)
			}

			account := readRecoveryAccount(t, view, sourceKey)
			if account.Balance != 1_000_000-xrpOverflowFee {
				t.Fatalf("payer balance = %d, want %d", account.Balance, 1_000_000-xrpOverflowFee)
			}
			if test.ticket {
				if account.Sequence != 3 {
					t.Fatalf("ticket payer sequence = %d, want 3", account.Sequence)
				}
				if account.TicketCount != 0 || account.OwnerCount != 0 {
					t.Fatalf("ticket payer counts = ticket %d/owner %d, want 0/0", account.TicketCount, account.OwnerCount)
				}
				if data, err := view.Read(ticketKey); err != nil || data != nil {
					t.Fatalf("ticket after recovery = %x, err=%v, want deleted", data, err)
				}
			} else if account.Sequence != 2 {
				t.Fatalf("sequence payer sequence = %d, want 2", account.Sequence)
			}

			for i, target := range targets {
				data, err := view.Read(target.key)
				if err != nil {
					t.Fatalf("read target %d after recovery: %v", i, err)
				}
				if !bytes.Equal(data, target.before) {
					t.Fatalf("target %d changed despite invariant recovery", i)
				}
			}
		})
	}
}

func TestApplyXRPNotCreated_ActualSecondPassEscalates(t *testing.T) {
	for _, test := range []struct {
		name   string
		ticket bool
	}{
		{name: "sequence", ticket: false},
		{name: "ticket", ticket: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			view := newRecordingBaseView()
			var sourceKey, ticketKey, ownerDirKey keylet.Keylet
			if test.ticket {
				sourceKey, ticketKey, ownerDirKey = seedXRPOverflowTicketAccount(t, view)
			} else {
				sourceKey = fundRecoveryAccount(t, view, 1_000_000, 1)
			}
			targets := seedXRPOverflowTargets(t, view)
			txn := newXRPOverflowTx(targets, test.ticket)
			rules := amendment.AllSupportedRules()

			var ticketBefore, ownerDirBefore []byte
			if test.ticket {
				var err error
				ticketBefore, err = view.Read(ticketKey)
				if err != nil || ticketBefore == nil {
					t.Fatalf("read ticket before terminal failure: data=%x err=%v", ticketBefore, err)
				}
				ownerDirBefore, err = view.Read(ownerDirKey)
				if err != nil || ownerDirBefore == nil {
					t.Fatalf("read owner directory before terminal failure: data=%x err=%v", ownerDirBefore, err)
				}
			}

			var hookCalls int
			engine := recoveryEngine(view, txcore.TapNONE)
			engine.SetInvariantViolationHookForTest(func(result ter.Result, table *applystate.ApplyStateTable) *InvariantViolationValue {
				if result != ter.TecINVARIANT_FAILED {
					return nil
				}
				hookCalls++
				// Inject a fee-only change, then classify it with the real checker.
				data, err := table.Read(targets[0].key)
				if err != nil || data == nil {
					t.Fatalf("read fee-only target: data=%x err=%v", data, err)
				}
				account, err := state.ParseAccountRoot(data)
				if err != nil {
					t.Fatalf("parse fee-only target: %v", err)
				}
				account.Balance++
				invalid, err := state.SerializeAccountRoot(account)
				if err != nil {
					t.Fatalf("serialize fee-only XRP increase: %v", err)
				}
				if err := table.Update(targets[0].key, invalid); err != nil {
					t.Fatalf("inject fee-only target: %v", err)
				}
				violation := invariants.CheckInvariants(
					wrapTxForInvariants(txn),
					invariants.Result(result),
					xrpOverflowFee,
					xrpOverflowFee,
					table.CollectEntries(),
					table,
					rules,
				)
				if violation == nil || violation.Name != "XRPNotCreated" {
					t.Fatalf("real fee-only checker violation = %v, want XRPNotCreated", violation)
				}
				return violation
			})

			result := engine.Apply(txn)
			if result.Result != ter.TefINVARIANT_FAILED || result.Applied {
				t.Fatalf("result/applied = %s/%v, want tefINVARIANT_FAILED/false", result.Result, result.Applied)
			}
			if result.Fee != xrpOverflowFee || view.destroyed != 0 {
				t.Fatalf("terminal invariant failure charged fee/result fee = %d/%d, want 0/%d", view.destroyed, result.Fee, xrpOverflowFee)
			}
			if hookCalls != 1 {
				t.Fatalf("fee-only invariant hook calls = %d, want 1", hookCalls)
			}
			account := readRecoveryAccount(t, view, sourceKey)
			wantSequence := uint32(1)
			if test.ticket {
				wantSequence = 3
			}
			if account.Balance != 1_000_000 || account.Sequence != wantSequence {
				t.Fatalf("payer balance/sequence = %d/%d, want 1000000/%d", account.Balance, account.Sequence, wantSequence)
			}
			if test.ticket {
				if account.TicketCount != 1 || account.OwnerCount != 1 {
					t.Fatalf("ticket payer counts = ticket %d/owner %d, want 1/1", account.TicketCount, account.OwnerCount)
				}
				data, err := view.Read(ticketKey)
				if err != nil || !bytes.Equal(data, ticketBefore) {
					t.Fatalf("ticket changed after terminal failure: data=%x err=%v", data, err)
				}
				data, err = view.Read(ownerDirKey)
				if err != nil || !bytes.Equal(data, ownerDirBefore) {
					t.Fatalf("owner directory changed after terminal failure: data=%x err=%v", data, err)
				}
			}
			for i, target := range targets {
				data, err := view.Read(target.key)
				if err != nil {
					t.Fatalf("read target %d after terminal failure: %v", i, err)
				}
				if !bytes.Equal(data, target.before) {
					t.Fatalf("target %d changed after terminal invariant failure", i)
				}
			}
		})
	}
}

func newXRPOverflowTx(targets []xrpOverflowTarget, ticket bool) *xrpOverflowTx {
	base := recoveryTx(xrpOverflowFee, 1)
	if ticket {
		sequence := uint32(0)
		ticketSequence := uint32(2)
		base.Sequence = &sequence
		base.TicketSequence = &ticketSequence
	}
	return &xrpOverflowTx{BaseTx: base, targets: targets}
}

func seedXRPOverflowTargets(t *testing.T, view interface {
	Insert(keylet.Keylet, []byte) error
}) []xrpOverflowTarget {
	t.Helper()
	perDelta := uint64(xrpOverflowTotalDelta / xrpOverflowAccountCount)
	remainder := uint64(xrpOverflowTotalDelta % xrpOverflowAccountCount)
	targets := make([]xrpOverflowTarget, xrpOverflowAccountCount)
	deltaSum := new(big.Int)
	wantDeltaSum := new(big.Int).Lsh(big.NewInt(1), 64)

	for i := range targets {
		var id [20]byte
		id[0] = 0xA5
		binary.BigEndian.PutUint32(id[16:], uint32(i+1))
		address := state.EncodeAccountIDSafe(id)
		if address == "" {
			t.Fatalf("encode target %d account ID", i)
		}

		before := uint64(1)
		after := before + perDelta
		if uint64(i) < remainder {
			after++
		}
		if after > invariants.InitialXRP {
			t.Fatalf("target %d after balance = %d exceeds InitialXRP", i, after)
		}
		beforeData, err := state.SerializeAccountRoot(&state.AccountRoot{
			Account:  address,
			Balance:  before,
			Sequence: 1,
		})
		if err != nil {
			t.Fatalf("serialize target %d before image: %v", i, err)
		}
		afterData, err := state.SerializeAccountRoot(&state.AccountRoot{
			Account:  address,
			Balance:  after,
			Sequence: 1,
		})
		if err != nil {
			t.Fatalf("serialize target %d after image: %v", i, err)
		}
		key := keylet.Account(id)
		if err := view.Insert(key, beforeData); err != nil {
			t.Fatalf("insert target %d: %v", i, err)
		}
		targets[i] = xrpOverflowTarget{key: key, before: beforeData, afterData: afterData}
		deltaSum.Add(deltaSum, new(big.Int).SetUint64(after-before))
	}
	if deltaSum.Cmp(wantDeltaSum) != 0 {
		t.Fatalf("positive AccountRoot delta = %s, want 2^64", deltaSum)
	}
	return targets
}

func seedXRPOverflowTicketAccount(t *testing.T, view *recordingBaseView) (keylet.Keylet, keylet.Keylet, keylet.Keylet) {
	t.Helper()
	accountID, err := state.DecodeAccountID(recoveryTestAccount)
	if err != nil {
		t.Fatalf("decode ticket payer: %v", err)
	}
	accountKey := keylet.Account(accountID)
	accountData, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:     recoveryTestAccount,
		Balance:     1_000_000,
		Sequence:    3,
		OwnerCount:  1,
		TicketCount: 1,
	})
	if err != nil {
		t.Fatalf("serialize ticket payer: %v", err)
	}
	if err := view.Insert(accountKey, accountData); err != nil {
		t.Fatalf("insert ticket payer: %v", err)
	}

	ticketKey := keylet.Ticket(accountID, 2)
	dirResult, err := state.DirInsert(view, keylet.OwnerDir(accountID), ticketKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = accountID
	})
	if err != nil {
		t.Fatalf("insert ticket owner directory: %v", err)
	}
	ticketData, err := state.SerializeTicket(accountID, 2, dirResult.Page)
	if err != nil {
		t.Fatalf("serialize ticket: %v", err)
	}
	if err := view.Insert(ticketKey, ticketData); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}
	return accountKey, ticketKey, keylet.OwnerDir(accountID)
}
