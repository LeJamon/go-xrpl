package handlers

import (
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// ServerStateMethod handles the server_state RPC method
type ServerStateMethod struct{ BaseHandler }

func (m *ServerStateMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if err := RequireLedgerService(); err != nil {
		return nil, err
	}

	// Get server info from ledger service
	serverInfo := types.Services.Ledger.GetServerInfo()

	// Get fee settings
	baseFee, reserveBase, reserveIncrement := types.Services.Ledger.GetCurrentFees()

	// Calculate uptime
	uptime := int64(time.Since(serverStartTime).Seconds())

	// Build complete ledgers string
	completeLedgers := serverInfo.CompleteLedgers
	if completeLedgers == "" {
		completeLedgers = "empty"
	}

	// Get validated ledger info
	validatedLedgerHash := hex.EncodeToString(serverInfo.ValidatedLedgerHash[:])
	validatedLedgerSeq := serverInfo.ValidatedLedgerSeq

	// Determine server state
	serverState := "full"
	if serverInfo.Standalone {
		serverState = "standalone"
	}

	// Calculate base fee in XRP
	baseFeeXRP := float64(baseFee) / 1000000.0
	reserveBaseXRP := float64(reserveBase) / 1000000.0
	reserveIncXRP := float64(reserveIncrement) / 1000000.0

	response := map[string]interface{}{
		"state": map[string]interface{}{
			"build_version":     "2.0.0-goXRPLd",
			"complete_ledgers":  completeLedgers,
			"io_latency_ms":     1,
			"jq_trans_overflow": 0,
			"load_base":         256,
			"load_factor":       1.0,
			"peers":             0,
			"pubkey_node":       "n9KnrcCmL5psyKtk2KWP6jy14Hj4EXuZDg7XMdQJ9cSDoFSp53hu",
			"server_state":      serverState,
			"time":              time.Now().UTC().Format(time.RFC3339),
			"uptime":            uptime,
			"validated_ledger": map[string]interface{}{
				"age":              0,
				"base_fee_xrp":     baseFeeXRP,
				"hash":             validatedLedgerHash,
				"reserve_base_xrp": reserveBaseXRP,
				"reserve_inc_xrp":  reserveIncXRP,
				"seq":              validatedLedgerSeq,
			},
			"validation_quorum": 1,
		},
	}

	return response, nil
}

