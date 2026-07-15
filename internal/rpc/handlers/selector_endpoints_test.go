package handlers

import (
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/protocol"
)

type selectorEndpointReader struct {
	types.LedgerReader
	sequence  uint32
	hash      [32]byte
	closed    bool
	validated bool
}

func (r *selectorEndpointReader) Sequence() uint32       { return r.sequence }
func (r *selectorEndpointReader) Hash() [32]byte         { return r.hash }
func (r *selectorEndpointReader) IsClosed() bool         { return r.closed }
func (r *selectorEndpointReader) IsValidated() bool      { return r.validated }
func (r *selectorEndpointReader) ParentCloseTime() int64 { return 0 }

type selectorEndpointView struct {
	types.LedgerStateView
	id uint32
}

type selectorEndpointService struct {
	types.LedgerService
	current        uint32
	closed         uint32
	validated      uint32
	readersBySeq   map[uint32]*selectorEndpointReader
	readersByHash  map[[32]byte]*selectorEndpointReader
	viewsBySeq     map[uint32]*selectorEndpointView
	viewsByHash    map[[32]byte]*selectorEndpointView
	closedView     *selectorEndpointView
	readerSeqCalls []uint32
	viewSeqCalls   []uint32
	viewHashCalls  [][32]byte
	closedCalls    int
}

func (s *selectorEndpointService) GetCurrentLedgerIndex() uint32   { return s.current }
func (s *selectorEndpointService) GetClosedLedgerIndex() uint32    { return s.closed }
func (s *selectorEndpointService) GetValidatedLedgerIndex() uint32 { return s.validated }

func (s *selectorEndpointService) GetLedgerBySequence(sequence uint32) (types.LedgerReader, error) {
	s.readerSeqCalls = append(s.readerSeqCalls, sequence)
	reader := s.readersBySeq[sequence]
	if reader == nil {
		return nil, nil
	}
	return reader, nil
}

func (s *selectorEndpointService) GetLedgerByHash(hash [32]byte) (types.LedgerReader, error) {
	reader := s.readersByHash[hash]
	if reader == nil {
		return nil, nil
	}
	return reader, nil
}

func (s *selectorEndpointService) GetClosedLedgerView() (types.LedgerStateView, error) {
	s.closedCalls++
	return s.closedView, nil
}

func (s *selectorEndpointService) GetLedgerViewBySeq(sequence uint32) (types.LedgerStateView, types.LedgerReader, error) {
	s.viewSeqCalls = append(s.viewSeqCalls, sequence)
	view := s.viewsBySeq[sequence]
	reader := s.readersBySeq[sequence]
	if view == nil || reader == nil {
		return nil, nil, nil
	}
	return view, reader, nil
}

func (s *selectorEndpointService) GetLedgerViewByHash(hash [32]byte) (types.LedgerStateView, types.LedgerReader, error) {
	s.viewHashCalls = append(s.viewHashCalls, hash)
	view := s.viewsByHash[hash]
	reader := s.readersByHash[hash]
	if view == nil || reader == nil {
		return nil, nil, nil
	}
	return view, reader, nil
}

func newSelectorEndpointService(t *testing.T) (*selectorEndpointService, string) {
	t.Helper()
	const hashString = "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652"
	hash, err := protocol.Hash256FromHex(hashString)
	if err != nil {
		t.Fatal(err)
	}
	readers := map[uint32]*selectorEndpointReader{
		10: {sequence: 10, hash: [32]byte{10}, closed: false},
		9:  {sequence: 9, hash: [32]byte{9}, closed: true},
		8:  {sequence: 8, hash: [32]byte{8}, closed: true, validated: true},
		7:  {sequence: 7, hash: [32]byte{7}, closed: true, validated: true},
	}
	hashReader := &selectorEndpointReader{sequence: 6, hash: hash, closed: true, validated: true}
	return &selectorEndpointService{
		current:       10,
		closed:        9,
		validated:     8,
		readersBySeq:  readers,
		readersByHash: map[[32]byte]*selectorEndpointReader{hash: hashReader},
		viewsBySeq: map[uint32]*selectorEndpointView{
			10: {id: 10},
			9:  {id: 9},
			8:  {id: 8},
			7:  {id: 7},
		},
		viewsByHash: map[[32]byte]*selectorEndpointView{hash: {id: 6}},
		closedView:  &selectorEndpointView{id: 9},
	}, hashString
}

func TestResolvePathFindLedgerSelectors(t *testing.T) {
	t.Run("omitted uses closed view without metadata", func(t *testing.T) {
		service, _ := newSelectorEndpointService(t)
		ctx := &types.RpcContext{Services: &types.ServiceContainer{Ledger: service}}
		view, meta, rpcErr := resolvePathFindLedger(ctx, types.LedgerSpecifier{}, false)
		if rpcErr != nil || view != service.closedView || meta != nil {
			t.Fatalf("view=%#v meta=%#v error=%#v", view, meta, rpcErr)
		}
		if service.closedCalls != 1 || len(service.viewSeqCalls) != 0 || len(service.viewHashCalls) != 0 {
			t.Fatalf("unexpected calls: closed=%d seq=%v hash=%v", service.closedCalls, service.viewSeqCalls, service.viewHashCalls)
		}
	})

	tests := []struct {
		name      string
		probe     func(hash string) string
		wantView  uint32
		wantSeq   uint32
		wantOpen  bool
		validated bool
	}{
		{"current", func(string) string { return `{"ledger_index":"current"}` }, 10, 10, true, false},
		{"closed", func(string) string { return `{"ledger_index":"closed"}` }, 9, 9, false, false},
		{"validated", func(string) string { return `{"ledger_index":"validated"}` }, 8, 8, false, true},
		{"sequence", func(string) string { return `{"ledger_index":7}` }, 7, 7, false, true},
		{"hash", func(hash string) string { return `{"ledger_hash":"` + hash + `"}` }, 6, 6, false, true},
		{"legacy sequence", func(string) string { return `{"ledger":7}` }, 7, 7, false, true},
		{"legacy hash", func(hash string) string { return `{"ledger":"` + hash + `"}` }, 6, 6, false, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, hash := newSelectorEndpointService(t)
			ctx := &types.RpcContext{Services: &types.ServiceContainer{Ledger: service}}
			spec, hasSelector, parseErr := parseLedgerSpecifier(json.RawMessage(test.probe(hash)))
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			view, meta, rpcErr := resolvePathFindLedger(ctx, spec, hasSelector)
			if rpcErr != nil {
				t.Fatal(rpcErr)
			}
			selectedView, ok := view.(*selectorEndpointView)
			if !ok || selectedView.id != test.wantView {
				t.Fatalf("view = %#v, want %d", view, test.wantView)
			}
			if meta == nil || meta.seq != test.wantSeq || meta.current != test.wantOpen || meta.validated != test.validated {
				t.Fatalf("metadata = %#v", meta)
			}
		})
	}

	t.Run("conflicting selectors", func(t *testing.T) {
		_, hash := newSelectorEndpointService(t)
		_, _, rpcErr := parseLedgerSpecifier(json.RawMessage(`{"ledger_hash":"` + hash + `","ledger_index":7}`))
		if rpcErr == nil || rpcErr.Message != "Exactly one of 'ledger_hash' or 'ledger_index' can be specified." {
			t.Fatalf("error = %#v", rpcErr)
		}
	})
}

func TestAMMInfoResolvesSelectorBeforeParameters(t *testing.T) {
	method := &AMMInfoMethod{}

	t.Run("missing ledger outranks invalid AMM parameters", func(t *testing.T) {
		service, _ := newSelectorEndpointService(t)
		delete(service.readersBySeq, service.current)
		ctx := &types.RpcContext{Services: &types.ServiceContainer{Ledger: service}}
		_, rpcErr := method.Handle(ctx, json.RawMessage(`{}`))
		if rpcErr == nil || rpcErr.ErrorString != "noNetwork" {
			t.Fatalf("error = %#v", rpcErr)
		}
		if len(service.readerSeqCalls) != 1 {
			t.Fatalf("selector resolution calls = %v", service.readerSeqCalls)
		}
	})

	t.Run("valid selector is resolved once before parameter error", func(t *testing.T) {
		service, _ := newSelectorEndpointService(t)
		ctx := &types.RpcContext{Services: &types.ServiceContainer{Ledger: service}}
		_, rpcErr := method.Handle(ctx, json.RawMessage(`{}`))
		if rpcErr == nil || rpcErr.ErrorString != "invalidParams" {
			t.Fatalf("error = %#v", rpcErr)
		}
		if len(service.readerSeqCalls) != 1 || service.readerSeqCalls[0] != service.current {
			t.Fatalf("selector resolution calls = %v", service.readerSeqCalls)
		}
	})
}

func TestFillResolvedLedgerFields(t *testing.T) {
	open := &selectorEndpointReader{sequence: 10, hash: [32]byte{10}}
	response := map[string]any{}
	fillResolvedLedgerFields(response, open, false)
	if response["ledger_current_index"] != uint32(10) || response["validated"] != false {
		t.Fatalf("open response = %#v", response)
	}
	if _, ok := response["ledger_hash"]; ok {
		t.Fatalf("open response includes ledger_hash: %#v", response)
	}

	closed := &selectorEndpointReader{sequence: 9, hash: [32]byte{9}, closed: true, validated: true}
	response = map[string]any{}
	fillResolvedLedgerFields(response, closed, true)
	if response["ledger_index"] != uint32(9) || response["validated"] != true {
		t.Fatalf("closed response = %#v", response)
	}
	if response["ledger_hash"] != FormatLedgerHash(closed.hash) {
		t.Fatalf("closed response hash = %#v", response["ledger_hash"])
	}
}
