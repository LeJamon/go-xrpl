package engine

import "testing"

type balanceHookView struct {
	*mockBaseView
	want int64
}

func (v *balanceHookView) BalanceHookMPT([20]byte, [24]byte, int64) int64 {
	return v.want
}

func TestRulesViewBalanceHookMPT(t *testing.T) {
	account := [20]byte{1}
	id := [24]byte{2}

	withHook := rulesView{LedgerView: &balanceHookView{mockBaseView: newMockBaseView(), want: 7}}
	if got := withHook.BalanceHookMPT(account, id, 11); got != 7 {
		t.Fatalf("BalanceHookMPT() = %d, want 7", got)
	}

	withoutHook := rulesView{LedgerView: newMockBaseView()}
	if got := withoutHook.BalanceHookMPT(account, id, 11); got != 11 {
		t.Fatalf("BalanceHookMPT() without hook = %d, want 11", got)
	}
}
