package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// serverStartTime tracks when the server started for uptime calculation
var serverStartTime = time.Now()

// ServerInfoMethod handles the server_info RPC method
type ServerInfoMethod struct{}

func (m *ServerInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	// Check if ledger service is available
	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
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

	// Calculate base fee in XRP (drops / 1,000,000)
	baseFeeXRP := float64(baseFee) / 1000000.0
	reserveBaseXRP := float64(reserveBase) / 1000000.0
	reserveIncXRP := float64(reserveIncrement) / 1000000.0

	response := map[string]interface{}{
		"info": map[string]interface{}{
			"build_version":              "2.0.0-goXRPLd",
			"complete_ledgers":           completeLedgers,
			"hostid":                     "goXRPLd",
			"io_latency_ms":              1,
			"jq_trans_overflow":          "0",
			"last_close": map[string]interface{}{
				"converge_time_s": 0.0,
				"proposers":       0,
			},
			"load_factor":                1.0,
			"peer_disconnects":           "0",
			"peer_disconnects_resources": "0",
			"peers":                      0, // Standalone mode has no peers
			"pubkey_node":                "n9KnrcCmL5psyKtk2KWP6jy14Hj4EXuZDg7XMdQJ9cSDoFSp53hu",
			"server_state":               serverState,
			"server_state_duration_us":   fmt.Sprintf("%d", uptime*1000000),
			"state_accounting": map[string]interface{}{
				"connected": map[string]interface{}{
					"duration_us": "0",
					"transitions": "0",
				},
				"disconnected": map[string]interface{}{
					"duration_us": "0",
					"transitions": "0",
				},
				"full": map[string]interface{}{
					"duration_us": fmt.Sprintf("%d", uptime*1000000),
					"transitions": "1",
				},
				"syncing": map[string]interface{}{
					"duration_us": "0",
					"transitions": "0",
				},
				"tracking": map[string]interface{}{
					"duration_us": "0",
					"transitions": "0",
				},
			},
			"time":   time.Now().UTC().Format("2006-Jan-02 15:04:05.000000 MST"),
			"uptime": uptime,
			"validated_ledger": map[string]interface{}{
				"age":              0, // In standalone, always fresh
				"base_fee_xrp":     baseFeeXRP,
				"hash":             validatedLedgerHash,
				"reserve_base_xrp": reserveBaseXRP,
				"reserve_inc_xrp":  reserveIncXRP,
				"seq":              validatedLedgerSeq,
			},
			"validation_quorum": 1, // Standalone needs only 1
		},
	}

	return response, nil
}

func (m *ServerInfoMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *ServerInfoMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}
