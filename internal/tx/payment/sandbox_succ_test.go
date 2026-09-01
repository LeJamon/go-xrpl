package payment

import (
	"errors"
	"testing"
)

type sandboxSuccErrorView struct {
	*paymentMockLedgerView
	err        error
	failOnCall int
	calls      int
}

func (v *sandboxSuccErrorView) Succ(key [32]byte) ([32]byte, []byte, bool, error) {
	v.calls++
	if v.calls == v.failOnCall {
		return [32]byte{}, nil, false, v.err
	}
	return v.paymentMockLedgerView.Succ(key)
}

func sandboxSuccKey(b byte) [32]byte {
	var key [32]byte
	key[0] = b
	return key
}

func TestPaymentSandboxSuccPropagatesViewErrorWithLocalCandidate(t *testing.T) {
	sentinel := errors.New("view successor failed")
	view := &sandboxSuccErrorView{
		paymentMockLedgerView: newPaymentMockLedgerView(),
		err:                   sentinel,
		failOnCall:            1,
	}
	sandbox := NewPaymentSandbox(view)
	sandbox.insertions[sandboxSuccKey(2)] = []byte{2}

	gotKey, gotData, found, err := sandbox.Succ(sandboxSuccKey(0))
	if err != sentinel {
		t.Fatalf("Succ error = %v, want %v", err, sentinel)
	}
	if found || gotKey != ([32]byte{}) || gotData != nil {
		t.Fatalf("Succ returned successful result on error: key=%x data=%x found=%v", gotKey, gotData, found)
	}
}

func TestPaymentSandboxSuccPropagatesViewErrorAfterDeletedEntry(t *testing.T) {
	sentinel := errors.New("view successor retry failed")
	view := &sandboxSuccErrorView{
		paymentMockLedgerView: newPaymentMockLedgerView(),
		err:                   sentinel,
		failOnCall:            2,
	}
	view.data[sandboxSuccKey(1)] = []byte{1}
	sandbox := NewPaymentSandbox(view)
	sandbox.deletions[sandboxSuccKey(1)] = true
	sandbox.insertions[sandboxSuccKey(3)] = []byte{3}

	gotKey, gotData, found, err := sandbox.Succ(sandboxSuccKey(0))
	if err != sentinel {
		t.Fatalf("Succ error = %v, want %v", err, sentinel)
	}
	if found || gotKey != ([32]byte{}) || gotData != nil {
		t.Fatalf("Succ returned successful result on error: key=%x data=%x found=%v", gotKey, gotData, found)
	}
	if view.calls != 2 {
		t.Fatalf("view Succ calls = %d, want 2", view.calls)
	}
}
