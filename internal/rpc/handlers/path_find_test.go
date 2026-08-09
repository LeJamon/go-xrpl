package handlers

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
)

func pathFindTestContext(pathSearchMax int) *types.RpcContext {
	return &types.RpcContext{Services: types.NewTestServiceGraph(&types.ServiceContainer{
		Capabilities: types.RPCCapabilities{PathSearchMax: pathSearchMax},
	})}
}

func TestPathFindCapabilityPrecedesValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		handle func(*types.RpcContext, json.RawMessage) (any, *types.RpcError)
	}{
		{name: "path_find", handle: (&pathFindMethod{}).Handle},
		{name: "ripple_path_find", handle: (&ripplePathFindMethod{}).Handle},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, rpcErr := test.handle(pathFindTestContext(0), json.RawMessage("{not json"))
			if rpcErr == nil || rpcErr.ErrorString != "notSupported" {
				t.Fatalf("disabled path-finding error = %v, want notSupported", rpcErr)
			}
		})
	}
}

func TestRipplePathFindRequiredCondition(t *testing.T) {
	if got := (&ripplePathFindMethod{}).RequiredCondition(); got != types.NoCondition {
		t.Fatalf("RequiredCondition = %v, want NoCondition", got)
	}
}

func TestPathFindPlainValidationAndNoEvents(t *testing.T) {
	for _, test := range []struct {
		name   string
		params string
		want   string
	}{
		{name: "missing", params: `{}`, want: "invalidParams"},
		{name: "nonstring", params: `{"subcommand":7}`, want: "invalidParams"},
		{name: "malformed", params: `{not json`, want: "invalidParams"},
		{name: "known", params: `{"subcommand":"create"}`, want: "noEvents"},
		{name: "unknown", params: `{"subcommand":"future"}`, want: "noEvents"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, rpcErr := (&pathFindMethod{}).Handle(pathFindTestContext(3), json.RawMessage(test.params))
			if rpcErr == nil || rpcErr.ErrorString != test.want {
				t.Fatalf("path_find error = %v, want %s", rpcErr, test.want)
			}
			if test.want == "invalidParams" && rpcErr.Message != "Invalid parameters." {
				t.Fatalf("path_find invalid message = %q, want canonical", rpcErr.Message)
			}
		})
	}
}

type pathFindTestReader struct {
	types.LedgerReader
	sequence  uint32
	hash      [32]byte
	closed    bool
	validated bool
}

func (r *pathFindTestReader) Sequence() uint32  { return r.sequence }
func (r *pathFindTestReader) Hash() [32]byte    { return r.hash }
func (r *pathFindTestReader) IsClosed() bool    { return r.closed }
func (r *pathFindTestReader) IsValidated() bool { return r.validated }

type pathFindTestView struct {
	types.LedgerStateView
	existsFn func() (bool, error)
}

func (v *pathFindTestView) Exists(keylet.Keylet) (bool, error) {
	if v.existsFn == nil {
		return true, nil
	}
	return v.existsFn()
}

type pathFindTestLedger struct {
	types.LedgerService
	info   types.LedgerServerInfo
	view   types.LedgerStateView
	reader types.LedgerReader
}

func (s *pathFindTestLedger) GetServerInfo() types.LedgerServerInfo { return s.info }
func (s *pathFindTestLedger) GetClosedLedgerView() (types.LedgerStateView, error) {
	return s.view, nil
}
func (s *pathFindTestLedger) GetLedgerViewBySeq(seq uint32) (types.LedgerStateView, types.LedgerReader, error) {
	if seq != 7 {
		return nil, nil, errors.New("ledger not found")
	}
	return s.view, s.reader, nil
}
func (s *pathFindTestLedger) GetLedgerViewByHash(hash [32]byte) (types.LedgerStateView, types.LedgerReader, error) {
	if s.reader == nil || hash != s.reader.Hash() {
		return nil, nil, errors.New("ledger not found")
	}
	return s.view, s.reader, nil
}

func freshPathFindInfo() types.LedgerServerInfo {
	return types.LedgerServerInfo{
		HaveValidated:            true,
		ValidatedLedgerCloseTime: time.Now().Unix() - protocol.RippleEpochUnix,
	}
}

func TestRipplePathFindStaleDefaultUsesApiVersionError(t *testing.T) {
	for _, test := range []struct {
		name string
		api  int
		want string
	}{
		{name: "v1", api: types.ApiVersion1, want: "noNetwork"},
		{name: "v2", api: types.ApiVersion2, want: "notSynced"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ledger := &pathFindTestLedger{info: types.LedgerServerInfo{}}
			ctx := &types.RpcContext{
				ApiVersion: test.api,
				Services: types.NewTestServiceGraph(&types.ServiceContainer{
					Ledger:       ledger,
					Capabilities: types.RPCCapabilities{PathSearchMax: 3},
				}),
			}
			_, rpcErr := (&ripplePathFindMethod{}).Handle(ctx, json.RawMessage(`{}`))
			if rpcErr == nil || rpcErr.ErrorString != test.want {
				t.Fatalf("stale default error = %v, want %s", rpcErr, test.want)
			}
		})
	}
}

func TestRipplePathFindExplicitHistoricalBypassesStaleDefaultGate(t *testing.T) {
	view := &pathFindTestView{}
	reader := &pathFindTestReader{sequence: 7, closed: true, validated: true}
	ledger := &pathFindTestLedger{info: types.LedgerServerInfo{}, view: view, reader: reader}
	ctx := &types.RpcContext{
		ApiVersion: types.ApiVersion2,
		Services: types.NewTestServiceGraph(&types.ServiceContainer{
			Ledger:       ledger,
			Capabilities: types.RPCCapabilities{PathSearchMax: 3},
			ClientLoad:   types.NewClientLoadShedder(),
		}),
	}
	_, rpcErr := (&ripplePathFindMethod{}).Handle(ctx, json.RawMessage(`{"ledger_index":7}`))
	if rpcErr == nil || rpcErr.ErrorString != "srcActMissing" {
		t.Fatalf("explicit historical error = %v, want srcActMissing after lookup", rpcErr)
	}
	if got := ctx.Services.ClientLoad().PathfindActive(); got != 0 {
		t.Fatalf("pathfind admission leaked after validation error: %d", got)
	}
}

func TestRipplePathFindExistsErrorsAreInternal(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	for _, test := range []struct {
		name   string
		exists func() (bool, error)
	}{
		{name: "source", exists: func() (bool, error) { return false, errors.New("source storage failed") }},
		{name: "destination", exists: func() (bool, error) { return true, nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			existsFn := test.exists
			if test.name == "destination" {
				calls := 0
				existsFn = func() (bool, error) {
					calls++
					if calls == 1 {
						return true, nil
					}
					return false, errors.New("destination storage failed")
				}
			}
			view := &pathFindTestView{existsFn: existsFn}
			ledger := &pathFindTestLedger{info: freshPathFindInfo(), view: view}
			ctx := &types.RpcContext{Services: types.NewTestServiceGraph(&types.ServiceContainer{
				Ledger:       ledger,
				Capabilities: types.RPCCapabilities{PathSearchMax: 3},
			})}
			params := json.RawMessage(`{"source_account":"` + account + `","destination_account":"` + account + `","destination_amount":"10"}`)
			_, rpcErr := (&ripplePathFindMethod{}).Handle(ctx, params)
			if rpcErr == nil || rpcErr.ErrorString != "internal" || rpcErr.Message != "Internal error." {
				t.Fatalf("exists error = %v, want sanitized internal", rpcErr)
			}
		})
	}
}
