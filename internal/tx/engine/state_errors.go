package engine

import (
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/applystate"
	"github.com/LeJamon/go-xrpl/keylet"
)

// stateErrorRecorder keeps the first error returned by a ledger operation. A
// transaction handler can turn a view error into a TER without returning the
// error itself; recording at the view boundary preserves the underlying cause
// without treating ordinary protocol TERs as storage failures.
type stateErrorRecorder struct {
	err error
}

func (r *stateErrorRecorder) reset() {
	r.err = nil
}

func (r *stateErrorRecorder) record(err error) {
	if r.err == nil && err != nil {
		r.err = err
	}
}

func (r *stateErrorRecorder) cause() error {
	return r.err
}

func (e *Engine) resetStateErrors() {
	e.stateErrors.reset()
}

func (e *Engine) stateError() error {
	return e.stateErrors.cause()
}

type recordingAtomicView struct {
	applystate.AtomicLedgerView
	recorder *stateErrorRecorder
}

func (v *recordingAtomicView) Read(k keylet.Keylet) ([]byte, error) {
	data, err := v.AtomicLedgerView.Read(k)
	v.recorder.record(err)
	return data, err
}

func (v *recordingAtomicView) Exists(k keylet.Keylet) (bool, error) {
	exists, err := v.AtomicLedgerView.Exists(k)
	v.recorder.record(err)
	return exists, err
}

func (v *recordingAtomicView) Insert(k keylet.Keylet, data []byte) error {
	err := v.AtomicLedgerView.Insert(k, data)
	v.recorder.record(err)
	return err
}

func (v *recordingAtomicView) Update(k keylet.Keylet, data []byte) error {
	err := v.AtomicLedgerView.Update(k, data)
	v.recorder.record(err)
	return err
}

func (v *recordingAtomicView) Erase(k keylet.Keylet) error {
	err := v.AtomicLedgerView.Erase(k)
	v.recorder.record(err)
	return err
}

func (v *recordingAtomicView) AdjustDropsDestroyed(amount drops.XRPAmount) error {
	err := v.AtomicLedgerView.AdjustDropsDestroyed(amount)
	v.recorder.record(err)
	return err
}

func (v *recordingAtomicView) ApplyAtomically(apply func(ledger.Writer) error) error {
	err := v.AtomicLedgerView.ApplyAtomically(func(writer ledger.Writer) error {
		callbackErr := apply(recordingWriter{Writer: writer, recorder: v.recorder})
		if callbackErr != nil {
			return callbackErr
		}
		// A handler can ignore a read or writer error and return a normal TER.
		// Refuse the atomic commit while the failure is still inside the
		// underlying view so its rollback remains effective.
		return v.recorder.cause()
	})
	v.recorder.record(err)
	return err
}

func (v *recordingAtomicView) ForEach(fn func(key [32]byte, data []byte) bool) error {
	err := v.AtomicLedgerView.ForEach(fn)
	v.recorder.record(err)
	return err
}

func (v *recordingAtomicView) Succ(key [32]byte) ([32]byte, []byte, bool, error) {
	foundKey, data, found, err := v.AtomicLedgerView.Succ(key)
	v.recorder.record(err)
	return foundKey, data, found, err
}

func (v *recordingAtomicView) TxExists(txID [32]byte) (bool, error) {
	exists, err := v.AtomicLedgerView.TxExists(txID)
	v.recorder.record(err)
	return exists, err
}

type mptBalanceHook interface {
	BalanceHookMPT([20]byte, [24]byte, int64) int64
}

// recordingAtomicViewWithBalanceHook keeps the optional MPT hook available
// only when the wrapped view provides it.
type recordingAtomicViewWithBalanceHook struct {
	*recordingAtomicView
	hook mptBalanceHook
}

func (v *recordingAtomicViewWithBalanceHook) BalanceHookMPT(account [20]byte, id [24]byte, amount int64) int64 {
	return v.hook.BalanceHookMPT(account, id, amount)
}

type recordingWriter struct {
	ledger.Writer
	recorder *stateErrorRecorder
}

func (w recordingWriter) Insert(k keylet.Keylet, data []byte) error {
	err := w.Writer.Insert(k, data)
	w.recorder.record(err)
	return err
}

func (w recordingWriter) Update(k keylet.Keylet, data []byte) error {
	err := w.Writer.Update(k, data)
	w.recorder.record(err)
	return err
}

func (w recordingWriter) Erase(k keylet.Keylet) error {
	err := w.Writer.Erase(k)
	w.recorder.record(err)
	return err
}

func (w recordingWriter) AdjustDropsDestroyed(amount drops.XRPAmount) error {
	err := w.Writer.AdjustDropsDestroyed(amount)
	w.recorder.record(err)
	return err
}

// recordingPseudoView preserves the optional snapshot methods used by pseudo
// transaction application without making every recording view claim them.
type recordingPseudoView struct {
	*recordingAtomicView
	pseudo atomicPseudoView
}

type recordingPseudoViewWithBalanceHook struct {
	*recordingPseudoView
	hook mptBalanceHook
}

func (v *recordingPseudoViewWithBalanceHook) BalanceHookMPT(account [20]byte, id [24]byte, amount int64) int64 {
	return v.hook.BalanceHookMPT(account, id, amount)
}

func (v *recordingPseudoView) MutableSnapshot() (snapshot *ledger.Ledger, err error) {
	snapshot, err = v.pseudo.MutableSnapshot()
	v.recorder.record(err)
	return snapshot, err
}

func (v *recordingPseudoView) ConsumeState(snapshot *ledger.Ledger) error {
	err := v.pseudo.ConsumeState(snapshot)
	v.recorder.record(err)
	return err
}

var _ applystate.AtomicLedgerView = (*recordingAtomicView)(nil)
var _ applystate.AtomicLedgerView = (*recordingAtomicViewWithBalanceHook)(nil)
var _ ledger.Writer = recordingWriter{}
var _ applystate.AtomicLedgerView = (*recordingPseudoView)(nil)
var _ applystate.AtomicLedgerView = (*recordingPseudoViewWithBalanceHook)(nil)
var _ txcore.LedgerView = (*recordingAtomicView)(nil)
