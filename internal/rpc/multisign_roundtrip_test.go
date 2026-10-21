package rpc

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	rpcadapter "github.com/LeJamon/go-xrpl/internal/rpc/adapter"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
	"github.com/stretchr/testify/require"
)

func TestSignForSubmitMultisignedRoundTrip(t *testing.T) {
	for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2} {
		t.Run(fmt.Sprintf("api_v%d", apiVersion), func(t *testing.T) {
			db, err := sqlitedb.NewRepositoryManager(t.Context(), t.TempDir(), sqlitedb.Settings{})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			svc, err := service.New(service.Config{
				Standalone: true, NetworkID: 10000,
				GenesisConfig: genesis.DefaultConfig(), RelationalDB: db,
			})
			require.NoError(t, err)
			require.NoError(t, svc.Start())
			t.Cleanup(svc.Stop)
			registry, err := handlers.BuildRegistry()
			require.NoError(t, err)
			ctx := &types.RpcContext{
				Context: t.Context(), Role: types.RoleAdmin, ApiVersion: apiVersion,
				Services: types.NewTestServiceGraph(&types.ServiceContainer{
					Ledger:       rpcadapter.NewLedgerServiceAdapter(svc),
					Capabilities: types.RPCCapabilities{SigningEnabled: true},
				}),
			}
			call := func(method string, params map[string]any) map[string]any {
				t.Helper()
				handler, ok := registry.Get(method)
				require.True(t, ok)
				raw, err := json.Marshal(params)
				require.NoError(t, err)
				result, rpcErr := handler.Handle(ctx, raw)
				require.Nil(t, rpcErr, "%s: %v", method, rpcErr)
				raw, err = json.Marshal(result)
				require.NoError(t, err)
				var fields map[string]any
				require.NoError(t, json.Unmarshal(raw, &fields))
				return fields
			}
			closeLedger := func() {
				t.Helper()
				_, err := svc.AcceptLedger(t.Context())
				require.NoError(t, err)
			}

			const source = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
			wallet := call("wallet_propose", map[string]any{"passphrase": "multisign round trip"})
			signer := wallet["account_id"]
			for _, fields := range []map[string]any{
				{"TransactionType": "Payment", "Account": source, "Destination": signer, "Amount": "10000000000"},
				{"TransactionType": "SignerListSet", "Account": source, "SignerQuorum": 1,
					"SignerEntries": []any{map[string]any{"SignerEntry": map[string]any{"Account": signer, "SignerWeight": 1}}}},
			} {
				result := call("submit", map[string]any{"secret": "masterpassphrase", "tx_json": fields})
				require.Equal(t, "tesSUCCESS", result["engine_result"])
				closeLedger()
			}
			account := call("account_info", map[string]any{"account": source, "ledger_index": "current"})
			signed := call("sign_for", map[string]any{
				"account": signer, "secret": wallet["master_seed"],
				"tx_json": map[string]any{
					"TransactionType": "Payment", "Account": source, "Destination": signer,
					"Amount": "1000000", "Fee": "20", "NetworkID": 10000, "SigningPubKey": "",
					"Sequence": account["account_data"].(map[string]any)["Sequence"],
				},
			})
			fields := signed["tx_json"].(map[string]any)
			require.NotEmpty(t, signed["tx_blob"])
			signers := fields["Signers"].([]any)
			require.Len(t, signers, 1)
			require.Equal(t, signer, signers[0].(map[string]any)["Signer"].(map[string]any)["Account"])
			hash := signed["hash"]
			if apiVersion == types.ApiVersion1 {
				hash = fields["hash"]
			}
			require.NotEmpty(t, hash)
			require.Equal(t, "1000000", fields["DeliverMax"])

			submitted := call("submit_multisigned", map[string]any{"tx_json": signed["tx_json"]})
			require.Equal(t, "tesSUCCESS", submitted["engine_result"])
			require.Equal(t, signed["tx_blob"], submitted["tx_blob"])
			if apiVersion == types.ApiVersion1 {
				require.Equal(t, hash, submitted["tx_json"].(map[string]any)["hash"])
			} else {
				require.Equal(t, hash, submitted["hash"])
			}
			closeLedger()
			validated := call("tx", map[string]any{"transaction": hash})
			require.Equal(t, true, validated["validated"])
			require.Equal(t, "tesSUCCESS", validated["meta"].(map[string]any)["TransactionResult"])
		})
	}
}
