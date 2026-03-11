package rpc

import (
	"encoding/json"
	"strconv"
	"sync"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/payment"
	"github.com/LeJamon/goXRPLd/internal/tx/payment/pathfinder"
	rpctypes "github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// PathFindSession holds the state for a persistent WebSocket path_find session.
// Each WebSocket connection can have at most one active session (matching rippled).
// Reference: rippled PathRequest class + PathFind.cpp handler
type PathFindSession struct {
	mu sync.Mutex

	// Request parameters (immutable after creation)
	srcAccount    [20]byte
	dstAccount    [20]byte
	dstAmount     tx.Amount
	sendMax       *tx.Amount
	srcCurrencies []payment.Issue
	convertAll    bool

	// Original string representations for response formatting
	srcAccountStr string
	dstAccountStr string
	dstAmountRaw  json.RawMessage

	// Last computed result (updated on each ledger close)
	lastResult *pathfinder.PathRequestResult

	// Request ID from the original create command
	id interface{}
}

// pathFindCreateRequest represents the path_find create subcommand parameters.
type pathFindCreateRequest struct {
	Subcommand         string          `json:"subcommand"`
	SourceAccount      string          `json:"source_account"`
	DestinationAccount string          `json:"destination_account"`
	DestinationAmount  json.RawMessage `json:"destination_amount"`
	SendMax            json.RawMessage `json:"send_max,omitempty"`
	SourceCurrencies   []struct {
		Currency string `json:"currency"`
		Issuer   string `json:"issuer,omitempty"`
	} `json:"source_currencies,omitempty"`
}

// ParseAndCreateSession parses a path_find create request and creates a session.
// Returns the session and initial result, or an RPC error.
func ParseAndCreateSession(params json.RawMessage, id interface{}) (*PathFindSession, *rpctypes.RpcError) {
	var request pathFindCreateRequest
	if err := json.Unmarshal(params, &request); err != nil {
		return nil, rpctypes.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
	}

	// Validate required fields
	if request.SourceAccount == "" {
		return nil, rpctypes.RpcErrorInvalidParams("Missing field 'source_account'.")
	}
	if request.DestinationAccount == "" {
		return nil, rpctypes.RpcErrorInvalidParams("Missing field 'destination_account'.")
	}
	if request.DestinationAmount == nil {
		return nil, rpctypes.RpcErrorInvalidParams("Missing field 'destination_amount'.")
	}

	// Decode accounts
	srcAccount, err := state.DecodeAccountID(request.SourceAccount)
	if err != nil {
		return nil, rpctypes.NewRpcError(rpctypes.RpcACT_MALFORMED, "srcActMalformed", "invalidParams",
			"Source account is malformed.")
	}
	dstAccount, err := state.DecodeAccountID(request.DestinationAccount)
	if err != nil {
		return nil, rpctypes.NewRpcError(rpctypes.RpcACT_MALFORMED, "dstActMalformed", "invalidParams",
			"Destination account is malformed.")
	}

	// Parse destination amount
	dstAmount := parseSessionAmount(request.DestinationAmount)

	// Check for convert_all mode (destination_amount = "-1")
	convertAll := false
	var strVal string
	if json.Unmarshal(request.DestinationAmount, &strVal) == nil && strVal == "-1" {
		convertAll = true
	}

	// Parse optional send_max
	var sendMax *tx.Amount
	if request.SendMax != nil {
		amt := parseSessionAmount(request.SendMax)
		sendMax = &amt
	}

	// Parse optional source_currencies
	var srcCurrencies []payment.Issue
	for _, sc := range request.SourceCurrencies {
		issue := payment.Issue{Currency: sc.Currency}
		if sc.Issuer != "" {
			issuerID, decErr := state.DecodeAccountID(sc.Issuer)
			if decErr != nil {
				return nil, rpctypes.NewRpcError(rpctypes.RpcINVALID_PARAMS, "srcIsrMalformed", "invalidParams",
					"Source issuer is malformed.")
			}
			issue.Issuer = issuerID
		} else if sc.Currency != "XRP" && sc.Currency != "" {
			issue.Issuer = srcAccount
		}
		srcCurrencies = append(srcCurrencies, issue)
	}

	session := &PathFindSession{
		srcAccount:    srcAccount,
		dstAccount:    dstAccount,
		dstAmount:     dstAmount,
		sendMax:       sendMax,
		srcCurrencies: srcCurrencies,
		convertAll:    convertAll,
		srcAccountStr: request.SourceAccount,
		dstAccountStr: request.DestinationAccount,
		dstAmountRaw:  request.DestinationAmount,
		id:            id,
	}

	return session, nil
}

// Execute runs pathfinding against the given ledger view and stores the result.
// Returns the formatted PathFindEvent for sending to the client.
func (s *PathFindSession) Execute(view tx.LedgerView) *PathFindEvent {
	s.mu.Lock()
	defer s.mu.Unlock()

	pr := pathfinder.NewPathRequest(
		s.srcAccount, s.dstAccount,
		s.dstAmount, s.sendMax,
		s.srcCurrencies, s.convertAll,
	)
	result := pr.Execute(view)
	s.lastResult = result

	return s.buildEvent(result, true)
}

// GetLastResult returns the last computed result as a PathFindEvent (for status).
func (s *PathFindSession) GetLastResult() *PathFindEvent {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.lastResult == nil {
		return s.buildEvent(&pathfinder.PathRequestResult{}, false)
	}
	return s.buildEvent(s.lastResult, false)
}

// buildEvent formats a PathRequestResult into a PathFindEvent for the WebSocket client.
func (s *PathFindSession) buildEvent(result *pathfinder.PathRequestResult, fullReply bool) *PathFindEvent {
	alternatives := make([]PathAlternative, 0, len(result.Alternatives))
	for _, alt := range result.Alternatives {
		var srcAmtJSON json.RawMessage
		if alt.SourceAmount.IsNative() {
			srcAmtJSON, _ = json.Marshal(alt.SourceAmount.Value())
		} else {
			srcAmtJSON, _ = json.Marshal(map[string]string{
				"currency": alt.SourceAmount.Currency,
				"issuer":   alt.SourceAmount.Issuer,
				"value":    alt.SourceAmount.Value(),
			})
		}
		alternatives = append(alternatives, PathAlternative{
			SourceAmount:  srcAmtJSON,
			PathsComputed: convertToRPCPathSteps(alt.PathsComputed),
		})
	}

	return &PathFindEvent{
		Type:               "path_find",
		ID:                 s.id,
		SourceAccount:      s.srcAccountStr,
		DestinationAccount: s.dstAccountStr,
		DestinationAmount:  s.dstAmountRaw,
		FullReply:          fullReply,
		Alternatives:       alternatives,
	}
}

// convertToRPCPathSteps converts payment.PathStep slices to rpctypes.PathStep slices.
func convertToRPCPathSteps(paths [][]payment.PathStep) [][]rpctypes.PathStep {
	if len(paths) == 0 {
		return nil
	}
	result := make([][]rpctypes.PathStep, len(paths))
	for i, path := range paths {
		steps := make([]rpctypes.PathStep, len(path))
		for j, step := range path {
			steps[j] = rpctypes.PathStep{
				Account:  step.Account,
				Currency: step.Currency,
				Issuer:   step.Issuer,
				Type:     uint8(step.Type),
				TypeHex:  step.TypeHex,
			}
		}
		result[i] = steps
	}
	return result
}

// parseSessionAmount parses a JSON amount for path finding.
func parseSessionAmount(raw json.RawMessage) tx.Amount {
	var strVal string
	if err := json.Unmarshal(raw, &strVal); err == nil {
		drops, _ := strconv.ParseInt(strVal, 10, 64)
		return state.NewXRPAmountFromInt(drops)
	}

	var iou struct {
		Currency string `json:"currency"`
		Issuer   string `json:"issuer"`
		Value    string `json:"value"`
	}
	if err := json.Unmarshal(raw, &iou); err != nil {
		return state.NewXRPAmountFromInt(0)
	}

	if iou.Currency == "XRP" || iou.Currency == "" {
		drops, _ := strconv.ParseInt(iou.Value, 10, 64)
		return state.NewXRPAmountFromInt(drops)
	}

	return state.NewIssuedAmountFromDecimalString(iou.Value, iou.Currency, iou.Issuer)
}
