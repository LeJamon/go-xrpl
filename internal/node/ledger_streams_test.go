package node

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
)

type noValidatedLedgerInfoService struct {
	info service.ServerInfo
}

func (s noValidatedLedgerInfoService) GetValidatedLedger() *ledger.Ledger {
	return nil
}

func (s noValidatedLedgerInfoService) GetServerInfo() service.ServerInfo {
	return s.info
}

func TestLedgerInfoAdapterWithoutValidatedLedger(t *testing.T) {
	for _, test := range []struct {
		name             string
		serverState      string
		needsNetwork     bool
		completeLedgers  string
		expectedComplete string
	}{
		{name: "disconnected", serverState: "disconnected", completeLedgers: "1-2"},
		{name: "syncing", serverState: "syncing", completeLedgers: "1-2", expectedComplete: "1-2"},
		{name: "syncing needs network ledger", serverState: "syncing", needsNetwork: true, completeLedgers: "1-2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			info := (&ledgerInfoAdapter{ledgerService: noValidatedLedgerInfoService{info: service.ServerInfo{
				ServerState:        test.serverState,
				NeedsNetworkLedger: test.needsNetwork,
				CompleteLedgers:    test.completeLedgers,
				NetworkID:          42,
			}}}).GetCurrentLedgerInfo()
			if info == nil {
				t.Fatal("nil subscribe info without validated ledger")
			}
			if info.LedgerAvailable {
				t.Fatal("LedgerAvailable = true without validated ledger")
			}
			if info.ValidatedLedgers != test.expectedComplete {
				t.Fatalf("ValidatedLedgers = %q, want %q", info.ValidatedLedgers, test.expectedComplete)
			}
			wantPresent := test.serverState == "syncing" && !test.needsNetwork
			if info.ValidatedLedgersPresent != wantPresent {
				t.Fatalf("ValidatedLedgersPresent = %t, want %t", info.ValidatedLedgersPresent, wantPresent)
			}
			if info.NetworkID != 42 {
				t.Fatalf("NetworkID = %d, want 42", info.NetworkID)
			}
		})
	}
}
