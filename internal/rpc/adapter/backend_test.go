package adapter

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
)

type fakeBackend struct {
	Backend
	validated uint32
	status    replayfault.Status
}

func (f *fakeBackend) GetValidatedLedgerIndex() uint32       { return f.validated }
func (f *fakeBackend) ReplayFaultStatus() replayfault.Status { return f.status }
func (f *fakeBackend) ReplayBlocked() bool                   { return f.status.Blocked }

func TestLedgerServiceAdapterDelegatesToBackend(t *testing.T) {
	a := NewLedgerServiceAdapter(&fakeBackend{validated: 42, status: replayfault.Status{Blocked: true}})
	if got := a.GetValidatedLedgerIndex(); got != 42 {
		t.Fatalf("GetValidatedLedgerIndex = %d, want 42", got)
	}
	if !a.ReplayBlocked() || !a.ReplayFaultStatus().Blocked {
		t.Fatal("replay fault state not delegated to backend")
	}
}

func TestLedgerServiceAdapterTypedNilServiceIsUnavailable(t *testing.T) {
	var svc *service.Service
	a := NewLedgerServiceAdapter(svc)
	if a.svc != nil {
		t.Fatal("typed-nil service stored as non-nil backend")
	}
	if !a.ReplayBlocked() {
		t.Fatal("ReplayBlocked = false for unavailable service")
	}
	if status := a.ReplayFaultStatus(); !status.Blocked || status.PersistenceError == nil {
		t.Fatalf("ReplayFaultStatus = %+v, want blocked with persistence error", status)
	}
	if err := a.StartReplayFaultRecovery("id"); err == nil {
		t.Fatal("StartReplayFaultRecovery succeeded without a service")
	}
}
