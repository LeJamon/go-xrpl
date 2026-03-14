package handlers

import (
	"encoding/json"
	"strconv"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/LeJamon/goXRPLd/internal/tx/payment"
	"github.com/LeJamon/goXRPLd/internal/tx/payment/pathfinder"
)

// ripplePathFindRequest represents the ripple_path_find RPC request params.
type ripplePathFindRequest struct {
	SourceAccount      string          `json:"source_account"`
	DestinationAccount string          `json:"destination_account"`
	DestinationAmount  json.RawMessage `json:"destination_amount"`
	SendMax            json.RawMessage `json:"send_max,omitempty"`
	SourceCurrencies   []struct {
		Currency string `json:"currency"`
		Issuer   string `json:"issuer,omitempty"`
	} `json:"source_currencies,omitempty"`
}

// ripplePathFindResponse represents the ripple_path_find RPC response.
// Reference: rippled PathRequest::doUpdate() builds newStatus with these fields.
type ripplePathFindResponse struct {
	Alternatives          []pathAlternativeJSON `json:"alternatives"`
	DestinationAccount    string                `json:"destination_account"`
	DestinationAmount     interface{}           `json:"destination_amount"`
	DestinationCurrencies []string              `json:"destination_currencies"`
	FullReply             bool                  `json:"full_reply"`
	SourceAccount         string                `json:"source_account"`
}

type pathAlternativeJSON struct {
	PathsCanonical []interface{}        `json:"paths_canonical"`
	PathsComputed  [][]payment.PathStep `json:"paths_computed"`
	SourceAmount   interface{}          `json:"source_amount"`
}

// RipplePathFindMethod handles the ripple_path_find RPC method.
// Reference: rippled RipplePathFind.cpp
type RipplePathFindMethod struct{}

func (m *RipplePathFindMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request ripplePathFindRequest
	if err := json.Unmarshal(params, &request); err != nil {
		return nil, types.NewRpcError(types.RpcINVALID_PARAMS, "invalidParams", "invalidParams",
			"Invalid parameters: "+err.Error())
	}

	// Validate required fields
	if request.SourceAccount == "" {
		return nil, types.NewRpcError(types.RpcINVALID_PARAMS, "invalidParams", "invalidParams",
			"Missing field 'source_account'")
	}
	if request.DestinationAccount == "" {
		return nil, types.NewRpcError(types.RpcINVALID_PARAMS, "invalidParams", "invalidParams",
			"Missing field 'destination_account'")
	}
	if request.DestinationAmount == nil {
		return nil, types.NewRpcError(types.RpcINVALID_PARAMS, "invalidParams", "invalidParams",
			"Missing field 'destination_amount'")
	}

	// Decode accounts
	srcAccount, err := state.DecodeAccountID(request.SourceAccount)
	if err != nil {
		return nil, types.NewRpcError(types.RpcINVALID_PARAMS, "invalidParams", "invalidParams",
			"Invalid source_account")
	}
	dstAccount, err := state.DecodeAccountID(request.DestinationAccount)
	if err != nil {
		return nil, types.NewRpcError(types.RpcINVALID_PARAMS, "invalidParams", "invalidParams",
			"Invalid destination_account")
	}

	// Parse destination amount
	dstAmount := parsePathFindAmount(request.DestinationAmount)

	// Parse optional send_max
	var sendMax *state.Amount
	if request.SendMax != nil {
		amt := parsePathFindAmount(request.SendMax)
		sendMax = &amt
	}

	// Parse optional source_currencies
	var srcCurrencies []payment.Issue
	for _, sc := range request.SourceCurrencies {
		issue := payment.Issue{Currency: sc.Currency}
		if sc.Issuer != "" {
			issuerID, decErr := state.DecodeAccountID(sc.Issuer)
			if decErr != nil {
				return nil, types.NewRpcError(types.RpcINVALID_PARAMS, "invalidParams", "invalidParams",
					"Invalid source_currencies issuer")
			}
			issue.Issuer = issuerID
		} else if sc.Currency != "XRP" && sc.Currency != "" {
			issue.Issuer = srcAccount
		}
		srcCurrencies = append(srcCurrencies, issue)
	}

	// Get ledger view
	view, err := types.Services.Ledger.GetClosedLedgerView()
	if err != nil {
		return nil, types.NewRpcError(types.RpcNO_CURRENT, "noCurrent", "noCurrent",
			"No closed ledger available")
	}

	// Run pathfinding
	pr := pathfinder.NewPathRequest(srcAccount, dstAccount, dstAmount, sendMax, srcCurrencies, false)
	result := pr.Execute(view)

	// Build response matching rippled PathRequest::doUpdate() format.
	// Reference: rippled PathRequest.cpp lines 691-777
	response := ripplePathFindResponse{
		DestinationAccount:    request.DestinationAccount,
		DestinationAmount:     formatAmountJSON(dstAmount),
		DestinationCurrencies: result.DestinationCurrencies,
		FullReply:             true, // rippled sets !fast; legacy path always does a full reply
		SourceAccount:         request.SourceAccount,
	}

	for _, alt := range result.Alternatives {
		jAlt := pathAlternativeJSON{
			// paths_canonical is always an empty array for the legacy ripple_path_find API.
			// Reference: rippled PathRequest.cpp line 653
			PathsCanonical: []interface{}{},
			PathsComputed:  alt.PathsComputed,
			SourceAmount:   formatAmountJSON(alt.SourceAmount),
		}
		response.Alternatives = append(response.Alternatives, jAlt)
	}

	if response.Alternatives == nil {
		response.Alternatives = []pathAlternativeJSON{}
	}
	if response.DestinationCurrencies == nil {
		response.DestinationCurrencies = []string{}
	}

	return response, nil
}

func (m *RipplePathFindMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *RipplePathFindMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *RipplePathFindMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}

// formatAmountJSON formats an Amount for JSON output, matching rippled's
// STAmount::getJson(JsonOptions::none) behavior.
// XRP amounts are serialized as a string of drops.
// IOU amounts are serialized as {"currency": ..., "issuer": ..., "value": ...}.
func formatAmountJSON(amt state.Amount) interface{} {
	if amt.IsNative() {
		return amt.Value()
	}
	return map[string]string{
		"currency": amt.Currency,
		"issuer":   amt.Issuer,
		"value":    amt.Value(),
	}
}

// parsePathFindAmount parses a JSON amount for path finding.
func parsePathFindAmount(raw json.RawMessage) state.Amount {
	// Try as string first (XRP drops)
	var strVal string
	if err := json.Unmarshal(raw, &strVal); err == nil {
		drops, _ := strconv.ParseInt(strVal, 10, 64)
		return state.NewXRPAmountFromInt(drops)
	}

	// Try as IOU object
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
