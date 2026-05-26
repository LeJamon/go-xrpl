package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/LeJamon/goXRPLd/internal/tx/pseudo"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unsupportedAmendmentID is a hash absent from the registry, so isSupported()
// reports false — used to drive firstUnsupportedExpected.
var unsupportedAmendmentID = [32]byte{0xAB, 0xCD, 0xEF, 0x01}

// stubAmendmentsView is a minimal LedgerStateView that serves one Amendments
// SLE for Read(keylet.Amendments()); every other operation is a no-op.
type stubAmendmentsView struct {
	amendmentsData []byte
}

func (v *stubAmendmentsView) Read(k keylet.Keylet) ([]byte, error) {
	if k == keylet.Amendments() {
		return v.amendmentsData, nil
	}
	return nil, nil
}
func (v *stubAmendmentsView) Exists(keylet.Keylet) (bool, error)                 { return false, nil }
func (v *stubAmendmentsView) Insert(keylet.Keylet, []byte) error                 { return nil }
func (v *stubAmendmentsView) Update(keylet.Keylet, []byte) error                 { return nil }
func (v *stubAmendmentsView) Erase(keylet.Keylet) error                          { return nil }
func (v *stubAmendmentsView) ForEach(func(key [32]byte, data []byte) bool) error { return nil }
func (v *stubAmendmentsView) Succ([32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}
func (v *stubAmendmentsView) AdjustDropsDestroyed(drops.XRPAmount) {}
func (v *stubAmendmentsView) TxExists([32]byte) bool               { return false }
func (v *stubAmendmentsView) Rules() *amendment.Rules              { return nil }
func (v *stubAmendmentsView) LedgerSeq() uint32                    { return 0 }

// mockServerInfoWarnings adds a live amendment table to the server_info mock so
// buildAmendmentWarnings can read firstUnsupportedExpected.
type mockServerInfoWarnings struct {
	*mockLedgerServiceServerInfo
	table *amendment.AmendmentTable
}

func (m *mockServerInfoWarnings) AmendmentTable() *amendment.AmendmentTable { return m.table }

// serverInfoWarnings runs server_info and returns the parsed warnings array.
func serverInfoWarnings(t *testing.T, mock *mockServerInfoWarnings, isAdmin bool) []map[string]interface{} {
	t.Helper()
	services := &types.ServiceContainer{Ledger: mock, NodePublicKey: testNodePublicKey()}
	method := &handlers.ServerInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		IsAdmin:    isAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	result, rpcErr := method.Handle(ctx, nil)
	require.Nil(t, rpcErr)
	require.NotNil(t, result)

	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(resultJSON, &resp))

	raw, ok := resp["warnings"].([]interface{})
	if !ok {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(raw))
	for _, w := range raw {
		out = append(out, w.(map[string]interface{}))
	}
	return out
}

func warningByID(warnings []map[string]interface{}, id int) map[string]interface{} {
	for _, w := range warnings {
		if int(w["id"].(float64)) == id {
			return w
		}
	}
	return nil
}

// TestServerInfoAmendmentWarnings exercises the server_info warning wire shape
// end-to-end through ServerInfoMethod.Handle. Mirrors rippled
// NetworkOPsImp::getServerInfo (NetworkOPs.cpp:2644-2676).
func TestServerInfoAmendmentWarnings(t *testing.T) {
	// Build a table holding an unsupported amendment in majority (not enabled),
	// so firstUnsupportedExpected is set but the node is not blocked.
	tbl := amendment.NewAmendmentTable()
	const majorityClose uint32 = 800_000_000
	tbl.DoValidatedLedger(256, nil, map[[32]byte]uint32{unsupportedAmendmentID: majorityClose})
	expDate, ok := tbl.FirstUnsupportedExpected()
	require.True(t, ok, "test setup: firstUnsupportedExpected must be set")
	wantUTC := time.Unix(int64(expDate)+protocol.RippleEpochUnix, 0).UTC().Format("2006-Jan-02 15:04:05 UTC")

	t.Run("admin sees unsupported-majority (1001) with rippled date format", func(t *testing.T) {
		mock := &mockServerInfoWarnings{mockLedgerServiceServerInfo: newMockLedgerServiceServerInfo(), table: tbl}
		warnings := serverInfoWarnings(t, mock, true)

		w := warningByID(warnings, types.WarningUnsupportedAmendmentsMajority)
		require.NotNil(t, w, "admin must see the 1001 warning")
		assert.Equal(t,
			"One or more unsupported amendments have reached majority. Upgrade to the latest version before they are activated to avoid being amendment blocked.",
			w["message"])

		details := w["details"].(map[string]interface{})
		assert.Equal(t, float64(expDate), details["expected_date"],
			"expected_date is XRPL-epoch seconds (rippled time_since_epoch().count())")
		gotUTC, _ := details["expected_date_UTC"].(string)
		assert.Equal(t, wantUTC, gotUTC,
			"expected_date_UTC uses rippled's %%Y-%%b-%%d %%T %%Z form, not RFC3339")
		assert.Contains(t, gotUTC, "May",
			"rippled renders the abbreviated month name (%%b); RFC3339 would be numeric")

		assert.Nil(t, warningByID(warnings, types.WarningAmendmentBlocked),
			"node is not blocked, so no 1002")
	})

	t.Run("non-admin does not see 1001", func(t *testing.T) {
		mock := &mockServerInfoWarnings{mockLedgerServiceServerInfo: newMockLedgerServiceServerInfo(), table: tbl}
		warnings := serverInfoWarnings(t, mock, false)
		assert.Nil(t, warningByID(warnings, types.WarningUnsupportedAmendmentsMajority),
			"unsupported-majority warning is admin-only (rippled: admin && isAmendmentWarned)")
	})

	t.Run("blocked node emits 1002 and suppresses 1001", func(t *testing.T) {
		mock := &mockServerInfoWarnings{mockLedgerServiceServerInfo: newMockLedgerServiceServerInfo(), table: tbl}
		mock.amendmentBlocked = true
		warnings := serverInfoWarnings(t, mock, true) // admin, but blocked

		blocked := warningByID(warnings, types.WarningAmendmentBlocked)
		require.NotNil(t, blocked, "blocked node must emit 1002")
		assert.Equal(t,
			"This server is amendment blocked, and must be updated to be able to stay in sync with the network.",
			blocked["message"])

		assert.Nil(t, warningByID(warnings, types.WarningUnsupportedAmendmentsMajority),
			"1001 is suppressed while blocked (isAmendmentWarned == !blocked && warned)")
	})

	t.Run("healthy node emits no amendment warnings", func(t *testing.T) {
		mock := &mockServerInfoWarnings{mockLedgerServiceServerInfo: newMockLedgerServiceServerInfo(), table: amendment.NewAmendmentTable()}
		warnings := serverInfoWarnings(t, mock, true)
		assert.Nil(t, warningByID(warnings, types.WarningUnsupportedAmendmentsMajority))
		assert.Nil(t, warningByID(warnings, types.WarningAmendmentBlocked))
	})
}

// mockFeatureLedger serves an Amendments SLE so the feature handler can surface
// the per-amendment `majority` field end-to-end.
type mockFeatureLedger struct {
	*mockLedgerService
	view *stubAmendmentsView
}

func (m *mockFeatureLedger) GetClosedLedgerView() (types.LedgerStateView, error) {
	return m.view, nil
}

// TestFeatureMajorityFieldEndToEnd drives FeatureMethod.Handle against a ledger
// whose Amendments SLE lists an amendment in majority, and asserts the `majority`
// close time is surfaced. Mirrors rippled doFeature (Feature1.cpp:52,62,95).
func TestFeatureMajorityFieldEndToEnd(t *testing.T) {
	did := amendment.GetFeatureByName("DID")
	require.NotNil(t, did)

	const majorityClose uint32 = 700_000_000
	sleData, err := pseudo.SerializeAmendmentsSLE(&pseudo.AmendmentsSLE{
		// DID is in majority but NOT in the enabled set.
		Majorities: []pseudo.MajorityEntry{{Amendment: did.ID, CloseTime: majorityClose}},
	})
	require.NoError(t, err)

	mock := &mockFeatureLedger{
		mockLedgerService: newMockLedgerService(),
		view:              &stubAmendmentsView{amendmentsData: sleData},
	}
	services := &types.ServiceContainer{Ledger: mock}
	method := &handlers.FeatureMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest, // majority is emitted to all callers, not admin-gated
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	didHex := strings.ToUpper(hex.EncodeToString(did.ID[:]))

	t.Run("single feature lookup surfaces majority close time", func(t *testing.T) {
		params, _ := json.Marshal(map[string]interface{}{"feature": "DID"})
		result, rpcErr := method.Handle(ctx, params)
		require.Nil(t, rpcErr)

		resp := marshalToMap(t, result)
		feature := resp[didHex].(map[string]interface{})
		require.Contains(t, feature, "majority", "amendment in majority must report majority")
		assert.Equal(t, float64(majorityClose), feature["majority"])
		assert.Equal(t, false, feature["enabled"], "DID is not in the enabled set")
	})

	t.Run("all-features response surfaces majority only for the majority amendment", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resp := marshalToMap(t, result)
		features := resp["features"].(map[string]interface{})

		didEntry := features[didHex].(map[string]interface{})
		assert.Equal(t, float64(majorityClose), didEntry["majority"])

		// A different, not-in-majority amendment must omit the field entirely.
		var other *amendment.Feature
		for _, f := range amendment.AllFeatures() {
			if f.ID != did.ID {
				other = f
				break
			}
		}
		require.NotNil(t, other)
		otherHex := strings.ToUpper(hex.EncodeToString(other.ID[:]))
		assert.NotContains(t, features[otherHex].(map[string]interface{}), "majority",
			"amendment not in majority must omit majority")
	})
}

func marshalToMap(t *testing.T, v interface{}) map[string]interface{} {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}
