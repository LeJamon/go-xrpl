package rpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	rpcadapter "github.com/LeJamon/go-xrpl/internal/rpc/adapter"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

const accountObjectsOwner = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

func accountObjectsDirectoryFixture(t *testing.T, missingOnly bool) (*service.Service, *types.RpcContext, [][32]byte, [][32]byte, map[int]map[string]any) {
	t.Helper()
	svc, err := service.New(service.Config{Standalone: true, GenesisConfig: genesis.DefaultConfig()})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	open := svc.GetOpenLedger()
	_, rawID, err := addresscodec.DecodeClassicAddressToAccountID(accountObjectsOwner)
	require.NoError(t, err)
	ownerID := [20]byte(rawID)
	root := keylet.OwnerDir(ownerID).Key
	dirs := [][32]byte{root, keylet.DirPage(root, 1).Key}
	keys := make([][32]byte, 9)
	objects := make(map[int]map[string]any)
	for i := range keys {
		keys[i] = keylet.Check(ownerID, uint32(i+1)).Key
		if missingOnly || i%2 == 0 {
			continue
		}
		object := map[string]any{
			"LedgerEntryType": "Check", "Account": accountObjectsOwner,
			"Flags": uint32(0), "Sequence": uint32(i + 1),
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK", "SendMax": "1000",
		}
		if i == 1 || i == 5 {
			object = map[string]any{
				"LedgerEntryType": "Offer", "Account": accountObjectsOwner,
				"Flags": uint32(0), "Sequence": uint32(i + 1),
				"TakerGets": "1000", "TakerPays": "2000",
			}
		}
		encoded, err := binarycodec.Encode(object)
		require.NoError(t, err)
		data, err := hex.DecodeString(encoded)
		require.NoError(t, err)
		require.NoError(t, open.Insert(keylet.Keylet{Key: keys[i]}, data))
		objects[i], err = binarycodec.Decode(encoded)
		require.NoError(t, err)
		objects[i]["index"] = protocol.Hash256Hex(keys[i])
	}
	for i, indexes := range [][][32]byte{keys[:4], keys[4:]} {
		dir := &state.DirectoryNode{RootIndex: root, Owner: ownerID, Indexes: indexes}
		if i == 0 {
			dir.IndexNext = 1
			dir.IndexPrevious = 1
		}
		data, err := state.SerializeDirectoryNode(dir, false)
		require.NoError(t, err)
		require.NoError(t, open.Insert(keylet.Keylet{Key: dirs[i]}, data))
	}
	ctx := &types.RpcContext{
		Context: t.Context(), Role: types.RoleAdmin, ApiVersion: types.ApiVersion1,
		Services: types.NewTestServiceGraph(&types.ServiceContainer{Ledger: rpcadapter.NewLedgerServiceAdapter(svc)}),
	}
	return svc, ctx, dirs, keys, objects
}

func TestAccountObjectsMissingChildren(t *testing.T) {
	for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2} {
		for _, tc := range []struct {
			name           string
			fields         map[string]any
			missingOnly    bool
			directoryChild bool
			want           [][]int
		}{
			{name: "all objects", want: [][]int{{1, 3}, {5, 7}, {}}},
			{name: "type filter", fields: map[string]any{"type": "offer"}, want: [][]int{{1}, {5}, {}}},
			{name: "deletion blockers", fields: map[string]any{"deletion_blockers_only": true}, want: [][]int{{3}, {7}, {}}},
			{name: "sponsored filter", fields: map[string]any{"sponsored": true}, want: [][]int{{}, {}, {}}},
			{name: "all missing", missingOnly: true, want: [][]int{{}}},
			{name: "directory child", directoryChild: true, want: [][]int{{1, 3}, {5, 7}, {}}},
		} {
			t.Run(fmt.Sprintf("v%d/%s", apiVersion, tc.name), func(t *testing.T) {
				svc, ctx, dirs, keys, objects := accountObjectsDirectoryFixture(t, tc.missingOnly)
				if tc.directoryChild {
					data, err := state.SerializeDirectoryNode(&state.DirectoryNode{RootIndex: dirs[0]}, false)
					require.NoError(t, err)
					require.NoError(t, svc.GetOpenLedger().Insert(keylet.Keylet{Key: keys[0]}, data))
				}
				ctx.ApiVersion = apiVersion
				params := map[string]any{"account": accountObjectsOwner, "ledger_index": "current", "limit": 2}
				for k, v := range tc.fields {
					params[k] = v
				}
				for page, indexes := range tc.want {
					raw, err := json.Marshal(params)
					require.NoError(t, err)
					result, rpcErr := (&handlers.AccountObjectsMethod{}).Handle(ctx, raw)
					require.Nil(t, rpcErr)
					wantObjects := make([]map[string]any, 0, len(indexes))
					for _, i := range indexes {
						wantObjects = append(wantObjects, objects[i])
					}
					want := map[string]any{
						"account": accountObjectsOwner, "account_objects": wantObjects,
						"ledger_current_index": svc.GetOpenLedger().Sequence(), "validated": false,
					}
					if page+1 < len(tc.want) {
						marker := protocol.Hash256Hex(dirs[1]) + "," + protocol.Hash256Hex(keys[4+page*4])
						want["marker"], want["limit"] = marker, uint32(2)
						params["marker"] = marker
					}
					require.Equal(t, want, result, "page %d", page)
				}
			})
		}
	}
}

type accountObjectsFailingFamily struct {
	shamap.Family
	failHash [32]byte
	err      error
	failures int
}

func (f *accountObjectsFailingFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if hash == f.failHash {
		f.failures++
		return nil, f.err
	}
	return f.Family.Fetch(ctx, hash)
}

func TestAccountObjectsMissingChildrenPreserveStorageErrors(t *testing.T) {
	svc, ctx, _, keys, _ := accountObjectsDirectoryFixture(t, false)
	open := svc.GetOpenLedger()
	stateMap, err := open.StateMapSnapshot()
	require.NoError(t, err)
	root, err := stateMap.Hash()
	require.NoError(t, err)
	family := &accountObjectsFailingFamily{Family: backend.NewMemory(), err: errors.New("injected child storage failure")}
	require.NoError(t, stateMap.StoreDirty(func(entries []shamap.FlushEntry) error {
		for _, entry := range entries {
			if bytes.HasSuffix(entry.Data, keys[3][:]) {
				family.failHash = entry.Hash
			}
		}
		return family.StoreBatch(t.Context(), entries)
	}))
	require.NotZero(t, family.failHash)
	backed, err := shamap.NewFromRootHash(shamap.TypeState, root, family)
	require.NoError(t, err)
	replacement, err := ledger.NewOpenWithHeader(open.Header(), backed, shamap.New(shamap.TypeTransaction), open.Fees())
	require.NoError(t, err)
	require.NoError(t, open.AdoptState(replacement))

	result, queryErr := svc.GetAccountObjects(t.Context(), accountObjectsOwner, "current", "offer", 2, "")
	require.Nil(t, result)
	require.ErrorIs(t, queryErr, family.err)
	require.Positive(t, family.failures)
	before := family.failures
	response, rpcErr := (&handlers.AccountObjectsMethod{}).Handle(ctx, json.RawMessage(`{"account":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh","ledger_index":"current","type":"offer","limit":2}`))
	require.Nil(t, response)
	require.Equal(t, rpcerrors.RpcErrorInternal(), rpcErr)
	require.Greater(t, family.failures, before)
}
