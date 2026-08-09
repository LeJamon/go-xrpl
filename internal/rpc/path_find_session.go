package rpc

import (
	"encoding/json"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	rpctypes "github.com/LeJamon/go-xrpl/internal/rpc/types"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/payment/pathfinder"
	"github.com/LeJamon/go-xrpl/keylet"
)

// PathFindSession holds the state for a persistent WebSocket path_find session.
// Each WebSocket connection can have at most one active session (matching rippled).
// Reference: rippled PathRequest class + PathFind.cpp handler
type PathFindSession struct {
	mu             sync.Mutex
	computeMu      sync.Mutex
	computeFn      func(tx.LedgerView) *pathfinder.PathRequestResult
	request        *pathfinder.PathRequest
	searchLevelMax int

	// Request parameters (immutable after creation)
	srcAccount    [20]byte
	dstAccount    [20]byte
	dstAmount     tx.Amount
	sendMax       *tx.Amount
	srcCurrencies []payment.Issue
	convertAll    bool
	domainID      *[32]byte

	// Canonical account strings for response formatting
	srcAccountStr string
	dstAccountStr string

	// Last response state (updated on each ledger close)
	lastStatus *PathFindEvent

	// Request ID from the original create command
	id any
}

// pathFindCreateRequest represents the path_find create subcommand parameters.
type pathFindCreateRequest struct {
	Subcommand         string          `json:"subcommand"`
	SourceAccount      string          `json:"source_account"`
	DestinationAccount string          `json:"destination_account"`
	DestinationAmount  json.RawMessage `json:"destination_amount"`
	SendMax            json.RawMessage `json:"send_max,omitempty"`
	SourceCurrencies   json.RawMessage `json:"source_currencies,omitempty"`
	Domain             json.RawMessage `json:"domain,omitempty"`
}

// ParseAndCreateSession parses a path_find create request and creates a session.
// Returns the session and initial result, or an RPC error.
func ParseAndCreateSession(params json.RawMessage, id any) (*PathFindSession, *rpctypes.RpcError) {
	var request pathFindCreateRequest
	if err := json.Unmarshal(params, &request); err != nil {
		return nil, rpctypes.RpcErrorInvalidParams("Invalid parameters.")
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

	dstAmount, amtErr := state.AmountFromJSON(request.DestinationAmount)
	if amtErr != nil || !pathfinder.IsValidAsset(dstAmount) {
		return nil, rpctypes.RpcErrorDstAmtMalformed("Destination amount/currency/issuer is malformed.")
	}

	// destination_amount of exactly -1 selects convert-all mode.
	convertAll := dstAmount.Value() == "-1"
	if !convertAll && dstAmount.Signum() <= 0 {
		return nil, rpctypes.RpcErrorDstAmtMalformed("Destination amount/currency/issuer is malformed.")
	}

	// Parse optional send_max
	var sendMax *tx.Amount
	if request.SendMax != nil {
		// send_max requires destination_amount to be -1.
		if !convertAll {
			return nil, rpctypes.RpcErrorDstAmtMalformed("Destination amount/currency/issuer is malformed.")
		}
		amt, smErr := state.AmountFromJSON(request.SendMax)
		if smErr != nil || !pathfinder.IsValidAsset(amt) || (amt.Signum() <= 0 && amt.Value() != "-1") {
			return nil, rpctypes.RpcErrorSendMaxMalformed("SendMax amount malformed.")
		}
		sendMax = &amt
	}

	// Parse optional source_currencies
	var srcCurrencies []payment.Issue
	if request.SourceCurrencies != nil {
		var entries []json.RawMessage
		if err := json.Unmarshal(request.SourceCurrencies, &entries); err != nil || len(entries) == 0 || len(entries) > 18 {
			return nil, rpctypes.RpcErrorSrcCurMalformed("Source currency is malformed.")
		}
		var sendMaxIssue payment.Issue
		if sendMax != nil {
			sendMaxIssue = payment.GetIssue(*sendMax)
		}
		seen := make(map[payment.Issue]bool)
		add := func(issue payment.Issue) {
			if !seen[issue] {
				seen[issue] = true
				srcCurrencies = append(srcCurrencies, issue)
			}
		}

		for _, raw := range entries {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				return nil, rpctypes.RpcErrorSrcCurMalformed("Source currency is malformed.")
			}
			rawCurrency, hasCurrency := fields["currency"]
			rawMPT, hasMPT := fields["mpt_issuance_id"]
			if hasCurrency == hasMPT {
				return nil, rpctypes.RpcErrorSrcCurMalformed("Source currency is malformed.")
			}
			if hasMPT {
				if _, hasIssuer := fields["issuer"]; hasIssuer {
					return nil, rpctypes.RpcErrorSrcCurMalformed("Source currency is malformed.")
				}
				var value string
				if err := json.Unmarshal(rawMPT, &value); err != nil {
					return nil, rpctypes.RpcErrorSrcCurMalformed("Source currency is malformed.")
				}
				mptID, ok := pathfinder.ParseSourceMPTID(value)
				if !ok {
					return nil, rpctypes.RpcErrorSrcCurMalformed("Source currency is malformed.")
				}
				issue := payment.NewMPTIssue(mptID)
				if sendMax != nil {
					if !issue.Equal(sendMaxIssue) {
						continue
					}
					if sendMaxIssue.Issuer != srcAccount {
						return nil, rpctypes.RpcErrorSrcIsrMalformed("Source issuer is malformed.")
					}
				}
				add(issue)
				continue
			}

			var currency string
			if err := json.Unmarshal(rawCurrency, &currency); err != nil || !keylet.IsValidCurrencyCode(currency) {
				return nil, rpctypes.RpcErrorSrcCurMalformed("Source currency is malformed.")
			}
			if currency == "" {
				currency = "XRP"
			}
			var issuer [20]byte
			if rawIssuer, hasIssuer := fields["issuer"]; hasIssuer {
				var issuerString string
				if err := json.Unmarshal(rawIssuer, &issuerString); err != nil {
					return nil, rpctypes.RpcErrorSrcIsrMalformed("Source issuer is malformed.")
				}
				issuerID, decErr := state.DecodeAccountID(issuerString)
				if decErr != nil {
					return nil, rpctypes.RpcErrorSrcIsrMalformed("Source issuer is malformed.")
				}
				issuer = issuerID
			}
			if currency == "XRP" {
				if issuer != [20]byte{} {
					return nil, rpctypes.RpcErrorSrcCurMalformed("Source currency is malformed.")
				}
			} else if issuer == [20]byte{} {
				issuer = srcAccount
			}

			if sendMax != nil {
				if sendMaxIssue.IsMPT || currency != sendMaxIssue.Currency {
					continue
				}
				if issuer != srcAccount && sendMaxIssue.Issuer != srcAccount && issuer != sendMaxIssue.Issuer {
					return nil, rpctypes.RpcErrorSrcIsrMalformed("Source issuer is malformed.")
				}
				if issuer == srcAccount && sendMaxIssue.Issuer != srcAccount {
					issuer = sendMaxIssue.Issuer
				}
			}
			add(payment.Issue{Currency: currency, Issuer: issuer})
		}
	}

	var domainID *[32]byte
	if request.Domain != nil {
		var parsed bool
		domainID, parsed = rpctypes.ParsePathFindDomain(request.Domain)
		if !parsed {
			return nil, rpctypes.RpcErrorDomainMalformed("Domain is malformed.")
		}
	}

	pathRequest := pathfinder.NewPathRequest(
		srcAccount, dstAccount,
		dstAmount, sendMax,
		srcCurrencies, convertAll,
	)
	pathRequest.SetDomainID(domainID)
	pathRequest.SetSearchLevel(0)

	session := &PathFindSession{
		request:        pathRequest,
		srcAccount:     srcAccount,
		dstAccount:     dstAccount,
		dstAmount:      dstAmount,
		sendMax:        sendMax,
		srcCurrencies:  srcCurrencies,
		convertAll:     convertAll,
		domainID:       domainID,
		searchLevelMax: pathfinder.SearchLevelMax,
		srcAccountStr:  state.EncodeAccountIDSafe(srcAccount),
		dstAccountStr:  state.EncodeAccountIDSafe(dstAccount),
		id:             id,
	}

	return session, nil
}

func (s *PathFindSession) setSearchLevelMax(level int) {
	s.computeMu.Lock()
	defer s.computeMu.Unlock()
	s.searchLevelMax = level
	if s.request != nil {
		s.request.SetSearchLevelMax(level)
	}
}

// Compute runs pathfinding while serializing computations for this session.
// The returned result is not made visible through Status until CommitResult.
func (s *PathFindSession) Compute(view tx.LedgerView, fast bool) *pathfinder.PathRequestResult {
	s.computeMu.Lock()
	defer s.computeMu.Unlock()
	if s.computeFn != nil {
		return s.computeFn(view)
	}

	if s.request == nil {
		s.request = pathfinder.NewPathRequest(
			s.srcAccount, s.dstAccount,
			s.dstAmount, s.sendMax,
			s.srcCurrencies, s.convertAll,
		)
		s.request.SetDomainID(s.domainID)
		s.request.SetSearchLevel(0)
		s.request.SetSearchLevelMax(s.searchLevelMax)
	}
	return s.request.ExecuteUpdate(view, fast, false)
}

// Execute runs pathfinding against the given ledger view and stores the result.
// Full updates are returned in pushed-event form; the initial fast result is
// returned in response-result form.
func (s *PathFindSession) Execute(view tx.LedgerView, fullReply bool) *PathFindEvent {
	return s.CommitResult(s.Compute(view, !fullReply), fullReply)
}

// CommitResult publishes a computed path result as the session's latest
// status. Callers that need an external generation check should build an event
// first and use commitBuiltEvent while holding that check's locks.
func (s *PathFindSession) CommitResult(result *pathfinder.PathRequestResult, fullReply bool) *PathFindEvent {
	status := s.BuildEvent(result, fullReply)
	return s.commitBuiltEvent(status)
}

func (s *PathFindSession) commitBuiltEvent(status *PathFindEvent) *PathFindEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastStatus = clonePathFindEvent(status)
	event := clonePathFindEvent(status)
	if status.FullReply {
		event.Type = "path_find"
	}
	return event
}

// BuildEvent formats a path result without changing the session status.
func (s *PathFindSession) BuildEvent(result *pathfinder.PathRequestResult, fullReply bool) *PathFindEvent {
	if result == nil {
		result = &pathfinder.PathRequestResult{}
	}
	return s.buildEvent(result, fullReply)
}

// Status returns the stored response state with rippled's success marker.
func (s *PathFindSession) Status() *PathFindEvent {
	s.mu.Lock()
	defer s.mu.Unlock()

	status := s.statusLocked()
	status.Status = "success"
	return clonePathFindEvent(status)
}

// Close returns the stored response state with the closed marker set.
func (s *PathFindSession) Close() *PathFindEvent {
	s.mu.Lock()
	defer s.mu.Unlock()

	status := s.statusLocked()
	status.Closed = true
	return clonePathFindEvent(status)
}

func (s *PathFindSession) statusLocked() *PathFindEvent {
	if s.lastStatus == nil {
		s.lastStatus = s.buildEvent(&pathfinder.PathRequestResult{}, false)
	}
	return s.lastStatus
}

// buildEvent formats a PathRequestResult into a PathFindEvent for the WebSocket client.
func (s *PathFindSession) buildEvent(result *pathfinder.PathRequestResult, fullReply bool) *PathFindEvent {
	alternatives := make([]PathAlternative, 0, len(result.Alternatives))
	for _, alt := range result.Alternatives {
		alternative := PathAlternative{
			SourceAmount:  pathFindAmountJSON(alt.SourceAmount),
			PathsComputed: convertToRPCPathSteps(alt.PathsComputed),
		}
		if s.convertAll {
			alternative.DestinationAmount = pathFindAmountJSON(alt.DestinationAmount)
		}
		alternatives = append(alternatives, alternative)
	}

	return &PathFindEvent{
		ID:                 s.id,
		SourceAccount:      s.srcAccountStr,
		DestinationAccount: s.dstAccountStr,
		DestinationAmount:  pathFindAmountJSON(s.dstAmount),
		FullReply:          fullReply,
		Alternatives:       alternatives,
	}
}

func clonePathFindEvent(event *PathFindEvent) *PathFindEvent {
	clone := *event
	return &clone
}

func pathFindAmountJSON(amount tx.Amount) json.RawMessage {
	var value any
	if amount.IsNative() {
		value = amount.Value()
	} else if amount.IsMPT() {
		value = map[string]string{
			"mpt_issuance_id": amount.MPTIssuanceID(),
			"value":           amount.Value(),
		}
	} else {
		value = map[string]string{
			"currency": amount.Currency,
			"issuer":   amount.Issuer,
			"value":    amount.Value(),
		}
	}
	encoded, _ := json.Marshal(value)
	return encoded
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
				Account:       step.Account,
				Currency:      step.Currency,
				Issuer:        step.Issuer,
				MPTIssuanceID: step.MPTIssuanceID,
				Type:          uint8(step.Type),
				TypeHex:       step.TypeHex,
			}
		}
		result[i] = steps
	}
	return result
}
