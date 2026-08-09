package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/stretchr/testify/require"
)

type requestRejectAdvisory struct {
	setCalls int
	getCalls int
}

type requestRejectAmendmentController struct {
	types.LedgerService
	table    *amendment.Table
	setCalls int
}

func (c *requestRejectAmendmentController) Table() *amendment.Table { return c.table }
func (c *requestRejectAmendmentController) SetAmendmentVote(context.Context, [32]byte, bool) error {
	c.setCalls++
	return nil
}
func (*requestRejectAmendmentController) GetClosedLedgerView() (types.LedgerStateView, error) {
	return nil, nil
}

func (*requestRejectAdvisory) AdvisoryDelete() bool   { return true }
func (*requestRejectAdvisory) GetLastRotated() uint32 { return 0 }
func (s *requestRejectAdvisory) GetCanDelete() uint32 {
	s.getCalls++
	return 0
}
func (s *requestRejectAdvisory) SetCanDelete(uint32) (uint32, error) {
	s.setCalls++
	return 0, nil
}

func TestStrictRequestObjects(t *testing.T) {
	tests := []struct {
		name    string
		handler types.MethodHandler
		params  json.RawMessage
	}{
		{name: "feature malformed", handler: &FeatureMethod{}, params: json.RawMessage(`{"feature":`)},
		{name: "feature array", handler: &FeatureMethod{}, params: json.RawMessage(`[]`)},
		{name: "feature wrong field", handler: &FeatureMethod{}, params: json.RawMessage(`{"feature":1}`)},
		{name: "fetch info malformed", handler: &FetchInfoMethod{}, params: json.RawMessage(`{"clear":`)},
		{name: "fetch info null", handler: &FetchInfoMethod{}, params: json.RawMessage(`null`)},
		{name: "log level malformed", handler: &LogLevelMethod{}, params: json.RawMessage(`{"severity":`)},
		{name: "log level scalar", handler: &LogLevelMethod{}, params: json.RawMessage(`7`)},
		{name: "log level object field", handler: &LogLevelMethod{}, params: json.RawMessage(`{"severity":{}}`)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := &types.RpcContext{Context: context.Background(), IsAdmin: true}
			result, rpcErr := test.handler.Handle(ctx, test.params)
			require.Nil(t, result)
			require.NotNil(t, rpcErr)
			require.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
			require.Equal(t, "invalidParams", rpcErr.ErrorString)
			require.Equal(t, "Invalid parameters.", rpcErr.Message)
		})
	}
}

func TestDecodeRejectionDoesNotInvokeCallbacks(t *testing.T) {
	controller := &requestRejectAmendmentController{table: amendment.NewTable()}
	_, rpcErr := (&FeatureMethod{}).Handle(
		&types.RpcContext{Context: context.Background(), IsAdmin: true, Services: &types.ServiceContainer{Ledger: controller}},
		json.RawMessage(`{"feature":1,"vetoed":true}`),
	)
	require.NotNil(t, rpcErr)
	require.Zero(t, controller.setCalls)

	clearCalls := 0
	fetchCalls := 0
	services := &types.ServiceContainer{
		FetchInfoClear: func() { clearCalls++ },
		FetchInfo: func() map[string]any {
			fetchCalls++
			return nil
		},
	}
	_, rpcErr = (&FetchInfoMethod{}).Handle(
		&types.RpcContext{Services: services},
		json.RawMessage(`{"clear":true`),
	)
	require.NotNil(t, rpcErr)
	require.Zero(t, clearCalls)
	require.Zero(t, fetchCalls)

	store := &requestRejectAdvisory{}
	_, rpcErr = (&CanDeleteMethod{}).Handle(
		&types.RpcContext{Context: context.Background(), Services: &types.ServiceContainer{AdvisoryDeleteState: store}},
		json.RawMessage(`{"can_delete":true}`),
	)
	require.NotNil(t, rpcErr)
	require.Zero(t, store.getCalls)
	require.Zero(t, store.setCalls)
}

func TestRequestObjectJsonCppCoercions(t *testing.T) {
	clearCalls := 0
	services := &types.ServiceContainer{
		FetchInfoClear: func() { clearCalls++ },
		FetchInfo:      func() map[string]any { return nil },
	}
	result, rpcErr := (&FetchInfoMethod{}).Handle(
		&types.RpcContext{Services: services},
		json.RawMessage(`{"clear":"true"}`),
	)
	require.Nil(t, rpcErr)
	require.Equal(t, 1, clearCalls)
	require.Equal(t, true, result.(map[string]any)["clear"])

	controller := &requestRejectAmendmentController{table: amendment.NewTable()}
	_, rpcErr = (&FeatureMethod{}).Handle(
		&types.RpcContext{Context: context.Background(), IsAdmin: true, Services: &types.ServiceContainer{Ledger: controller}},
		json.RawMessage(`{"feature":"fixMasterKeyAsRegularKey","vetoed":"true"}`),
	)
	require.Nil(t, rpcErr)
	require.Equal(t, 1, controller.setCalls)

	controller = &requestRejectAmendmentController{table: amendment.NewTable()}
	result, rpcErr = (&FeatureMethod{}).Handle(
		&types.RpcContext{Context: context.Background(), IsAdmin: true, Services: &types.ServiceContainer{Ledger: controller}},
		json.RawMessage(`{"vetoed":true}`),
	)
	require.Nil(t, rpcErr)
	require.Zero(t, controller.setCalls)
	require.Contains(t, result.(map[string]any), "features")
}

func TestJsonCppRequestFields(t *testing.T) {
	boolTests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "null", raw: `null`},
		{name: "zero", raw: `0`},
		{name: "number", raw: `2`, want: true},
		{name: "empty string", raw: `""`},
		{name: "string", raw: `"false"`, want: true},
		{name: "empty array", raw: `[]`},
		{name: "array", raw: `[0]`, want: true},
		{name: "object", raw: `{"value":false}`, want: true},
	}
	for _, test := range boolTests {
		t.Run("bool "+test.name, func(t *testing.T) {
			var field jsonCppBoolField
			require.NoError(t, json.Unmarshal([]byte(test.raw), &field))
			require.True(t, field.present)
			require.Equal(t, test.want, field.value)
		})
	}

	stringTests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "null", raw: `null`},
		{name: "boolean", raw: `true`, want: "true"},
		{name: "integer", raw: `12`, want: "12"},
		{name: "string", raw: `"debug"`, want: "debug"},
	}
	for _, test := range stringTests {
		t.Run("string "+test.name, func(t *testing.T) {
			var field jsonCppStringField
			require.NoError(t, json.Unmarshal([]byte(test.raw), &field))
			require.True(t, field.present)
			require.Equal(t, test.want, field.value)
		})
	}

	var field jsonCppStringField
	require.Error(t, json.Unmarshal([]byte(`{}`), &field))
}

func TestCanDeleteNotEnabledPrecedesDecode(t *testing.T) {
	_, rpcErr := (&CanDeleteMethod{}).Handle(
		&types.RpcContext{Services: &types.ServiceContainer{}},
		json.RawMessage(`{"can_delete":`),
	)
	require.NotNil(t, rpcErr)
	require.Equal(t, types.RpcNOT_ENABLED, rpcErr.Code)
}

func TestCanDeleteStrictRequestObjects(t *testing.T) {
	tests := []struct {
		name   string
		params json.RawMessage
	}{
		{name: "malformed", params: json.RawMessage(`{"can_delete":`)},
		{name: "array", params: json.RawMessage(`[]`)},
		{name: "wrong field", params: json.RawMessage(`{"can_delete":true}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &requestRejectAdvisory{}
			_, rpcErr := (&CanDeleteMethod{}).Handle(
				&types.RpcContext{Context: context.Background(), Services: &types.ServiceContainer{AdvisoryDeleteState: store}},
				test.params,
			)
			require.NotNil(t, rpcErr)
			require.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
			require.Equal(t, "Invalid parameters.", rpcErr.Message)
			require.Zero(t, store.getCalls)
			require.Zero(t, store.setCalls)
		})
	}
}

func TestLogLevelDecodeRejectionDoesNotMutateLevels(t *testing.T) {
	globalBefore, partitionsBefore := xrpllog.Levels()
	_, rpcErr := (&LogLevelMethod{}).Handle(
		&types.RpcContext{},
		json.RawMessage(`{"severity":"debug","partition":{}}`),
	)
	require.NotNil(t, rpcErr)
	globalAfter, partitionsAfter := xrpllog.Levels()
	require.Equal(t, globalBefore, globalAfter)
	require.Equal(t, partitionsBefore, partitionsAfter)
}

func TestRequestObjectPreservesJsonCppIntegerRange(t *testing.T) {
	_, rpcErr := (&FetchInfoMethod{}).Handle(
		&types.RpcContext{},
		json.RawMessage(`{"unknown":4294967296}`),
	)
	require.NotNil(t, rpcErr)
	require.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
}
