package handlers

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

// AccountRoot flag constants.
const (
	lsfPasswordSpent            = entry.LsfPasswordSpent
	lsfRequireDestTag           = entry.LsfRequireDestTag
	lsfRequireAuth              = entry.LsfRequireAuth
	lsfDisallowXRP              = entry.LsfDisallowXRP
	lsfDisableMaster            = entry.LsfDisableMaster
	lsfNoFreeze                 = entry.LsfNoFreeze
	lsfGlobalFreeze             = entry.LsfGlobalFreeze
	lsfDefaultRipple            = entry.LsfDefaultRipple
	lsfDepositAuth              = entry.LsfDepositAuth
	lsfDisallowIncomingNFTOffer = entry.LsfDisallowIncomingNFTokenOffer
	lsfDisallowIncomingCheck    = entry.LsfDisallowIncomingCheck
	lsfDisallowIncomingPayChan  = entry.LsfDisallowIncomingPayChan
	lsfDisallowIncomingTrustln  = entry.LsfDisallowIncomingTrustline
	lsfAllowTrustLineClawback   = entry.LsfAllowTrustLineClawback
)

// AccountInfoMethod handles the account_info RPC method.
type AccountInfoMethod struct{ BaseHandler }

func (m *AccountInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	// Parse the raw JSON to inspect field types before struct unmarshaling.
	// This allows us to check for the "ident" alias and validate signer_lists type.
	var rawFields map[string]json.RawMessage
	if params != nil {
		if err := json.Unmarshal(params, &rawFields); err != nil {
			return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}

	var request struct {
		types.AccountParam
		types.LedgerSpecifier
		Queue       bool `json:"queue,omitempty"`
		SignerLists bool `json:"signer_lists,omitempty"`
		Strict      bool `json:"strict,omitempty"`
	}
	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	// Support "ident" as alias for "account" (matching rippled behavior)
	if request.Account == "" {
		if identRaw, ok := rawFields["ident"]; ok {
			var ident string
			if err := json.Unmarshal(identRaw, &ident); err != nil {
				// ident is present but not a string
				return nil, types.RpcErrorInvalidField("ident")
			}
			request.Account = ident
		}
	}

	if err := ValidateAccount(request.Account); err != nil {
		return nil, err
	}

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	// Determine ledger index. ledger_hash takes precedence over ledger_index
	// and is threaded through so the service resolves the specific named
	// ledger, mirroring rippled's ledgerFromRequest.
	ledgerIndex, selErr := resolveLedgerSelector(request.LedgerSpecifier)
	if selErr != nil {
		return nil, selErr
	}

	// Queue is only valid for the current (open) ledger.
	// Matching rippled: if queue=true but ledger is not open, return rpcINVALID_PARAMS.
	if request.Queue && ledgerIndex != "current" {
		return nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}

	// API v2: signer_lists must be a bool if present.
	// Reject non-bool values (string, number, etc.) matching rippled behavior.
	if ctx.ApiVersion > 1 {
		if signerListsRaw, ok := rawFields["signer_lists"]; ok {
			// Check if the raw JSON value is a boolean (true or false)
			trimmed := strings.TrimSpace(string(signerListsRaw))
			if trimmed != "true" && trimmed != "false" {
				return nil, types.RpcErrorInvalidParams("Invalid parameters.")
			}
		}
	}

	info, err := ctx.Services.Ledger.GetAccountInfo(ctx.Context, request.Account, ledgerIndex)
	if err != nil {
		if errors.Is(err, svcerr.ErrAccountNotFound) {
			return nil, types.RpcErrorActNotFound("Account not found.")
		}
		if errors.Is(err, svcerr.ErrLedgerNotFound) {
			return nil, types.RpcErrorLgrNotFound("Ledger not found.")
		}
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to get account info: %v", err))
	}

	// Build account_data by decoding the full SLE binary via binarycodec,
	// matching rippled's injectSLE which serializes all fields from the SLE.
	accountData := m.buildAccountData(info)

	// Build account_flags from Flags bitmask
	flags := info.Flags
	accountFlags := map[string]bool{
		"defaultRipple":         flags&lsfDefaultRipple != 0,
		"depositAuth":           flags&lsfDepositAuth != 0,
		"disableMasterKey":      flags&lsfDisableMaster != 0,
		"disallowIncomingXRP":   flags&lsfDisallowXRP != 0,
		"globalFreeze":          flags&lsfGlobalFreeze != 0,
		"noFreeze":              flags&lsfNoFreeze != 0,
		"passwordSpent":         flags&lsfPasswordSpent != 0,
		"requireAuthorization":  flags&lsfRequireAuth != 0,
		"requireDestinationTag": flags&lsfRequireDestTag != 0,
	}
	// Conditional flags (always include them — amendment gating is separate)
	accountFlags["disallowIncomingNFTokenOffer"] = flags&lsfDisallowIncomingNFTOffer != 0
	accountFlags["disallowIncomingCheck"] = flags&lsfDisallowIncomingCheck != 0
	accountFlags["disallowIncomingPayChan"] = flags&lsfDisallowIncomingPayChan != 0
	accountFlags["disallowIncomingTrustline"] = flags&lsfDisallowIncomingTrustln != 0
	accountFlags["allowTrustLineClawback"] = flags&lsfAllowTrustLineClawback != 0

	response := map[string]any{
		"account_data":  accountData,
		"account_flags": accountFlags,
	}
	fillLedgerFields(response, ledgerIndex, info.LedgerHash, info.LedgerIndex, info.Validated)

	// Add queue data if requested (only for current/open ledger — validated above)
	if request.Queue && ledgerIndex == "current" {
		response["queue_data"] = buildAccountQueueData(ctx.Services, request.Account)
	}

	if info.Index != "" {
		accountData["index"] = strings.ToUpper(info.Index)
	}

	// Load signer lists if requested
	if request.SignerLists {
		signerLists := m.loadSignerLists(ctx.Context, ctx.Services, request.Account, ledgerIndex)
		if ctx.ApiVersion > 1 {
			// API v2: signer_lists at top level
			response["signer_lists"] = signerLists
		} else {
			// API v1: nested under account_data
			accountData["signer_lists"] = signerLists
		}
	}

	return response, nil
}

// buildAccountData constructs account_data from the full SLE binary.
// When RawData is available, uses binarycodec.Decode to get all fields
// (matching rippled's injectSLE → sle.getJson). Falls back to manual
// construction from the AccountInfo struct fields if RawData is absent.
func (m *AccountInfoMethod) buildAccountData(info *types.AccountInfo) map[string]any {
	// Try full SLE decode from raw binary data
	if len(info.RawData) > 0 {
		hexData := hex.EncodeToString(info.RawData)
		decoded, err := binarycodec.Decode(hexData)
		if err == nil {
			return decoded
		}
		// Fall through to manual construction on decode error, but log
		// at debug — a silent fallback hid genuine codec bugs in the past.
		xrpllog.Named(xrpllog.PartitionRPC).Debug("account_info: SLE decode failed, falling back to struct",
			"account", info.Account, "err", err)
	}

	// Fallback: manually construct from AccountInfo struct fields
	accountData := map[string]any{
		"Account":         info.Account,
		"Balance":         info.Balance,
		"Flags":           info.Flags,
		"LedgerEntryType": "AccountRoot",
		"OwnerCount":      info.OwnerCount,
		"Sequence":        info.Sequence,
	}

	if info.RegularKey != "" {
		accountData["RegularKey"] = info.RegularKey
	}
	if info.Domain != "" {
		accountData["Domain"] = info.Domain
	}
	if info.EmailHash != "" {
		accountData["EmailHash"] = info.EmailHash
	}
	if info.TransferRate > 0 {
		accountData["TransferRate"] = info.TransferRate
	}
	if info.TickSize > 0 {
		accountData["TickSize"] = info.TickSize
	}
	if info.PreviousTxnID != "" {
		accountData["PreviousTxnID"] = info.PreviousTxnID
	}
	// Always include PreviousTxnLgrSeq when present (don't skip on 0)
	if info.PreviousTxnID != "" {
		accountData["PreviousTxnLgrSeq"] = info.PreviousTxnLgrSeq
	}

	return accountData
}

// buildAccountQueueData assembles the queue_data block for account_info from
// the live TxQ, mirroring rippled doAccountInfo (AccountInfo.cpp:193-283):
// per-tx seq/ticket, fee_level, optional LastLedgerSequence, fee,
// max_spend_drops and auth_change, plus the aggregate counts, sequence/ticket
// bounds, auth_change_queued and max_spend_drops_total. An empty (or unwired)
// queue yields {txn_count: 0}.
func buildAccountQueueData(services *types.ServiceContainer, account string) map[string]any {
	if services == nil || services.QueueAccountTxs == nil {
		return map[string]any{"txn_count": 0}
	}

	_, idBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
	if err != nil || len(idBytes) != 20 {
		return map[string]any{"txn_count": 0}
	}
	var accountID [20]byte
	copy(accountID[:], idBytes)

	txs := services.QueueAccountTxs(accountID)
	if len(txs) == 0 {
		return map[string]any{"txn_count": 0}
	}

	transactions := make([]any, 0, len(txs))
	var seqCount, ticketCount uint32
	var lowestSeq, highestSeq, lowestTicket, highestTicket *uint32
	anyAuthChanged := false
	var totalSpend uint64

	for _, tx := range txs {
		jvTx := map[string]any{}
		seqVal := tx.SeqValue
		if tx.IsTicket {
			jvTx["ticket"] = seqVal
			ticketCount++
			if lowestTicket == nil {
				v := seqVal
				lowestTicket = &v
			}
			h := seqVal
			highestTicket = &h
		} else {
			jvTx["seq"] = seqVal
			seqCount++
			if lowestSeq == nil {
				v := seqVal
				lowestSeq = &v
			}
			h := seqVal
			highestSeq = &h
		}

		jvTx["fee_level"] = strconv.FormatUint(tx.FeeLevel, 10)
		if tx.LastValid != 0 {
			jvTx["LastLedgerSequence"] = tx.LastValid
		}
		jvTx["fee"] = strconv.FormatUint(tx.Fee, 10)
		spend := tx.MaxSpendDrops
		jvTx["max_spend_drops"] = strconv.FormatUint(spend, 10)
		totalSpend += spend
		if tx.AuthChange {
			anyAuthChanged = true
		}
		jvTx["auth_change"] = tx.AuthChange

		transactions = append(transactions, jvTx)
	}

	queueData := map[string]any{
		"txn_count":             len(txs),
		"transactions":          transactions,
		"auth_change_queued":    anyAuthChanged,
		"max_spend_drops_total": strconv.FormatUint(totalSpend, 10),
	}
	if seqCount > 0 {
		queueData["sequence_count"] = seqCount
	}
	if ticketCount > 0 {
		queueData["ticket_count"] = ticketCount
	}
	if lowestSeq != nil {
		queueData["lowest_sequence"] = *lowestSeq
	}
	if highestSeq != nil {
		queueData["highest_sequence"] = *highestSeq
	}
	if lowestTicket != nil {
		queueData["lowest_ticket"] = *lowestTicket
	}
	if highestTicket != nil {
		queueData["highest_ticket"] = *highestTicket
	}
	return queueData
}

// loadSignerLists retrieves signer list objects for an account
func (m *AccountInfoMethod) loadSignerLists(ctx context.Context, services *types.ServiceContainer, account string, ledgerIndex string) []any {
	result, err := services.Ledger.GetAccountObjects(ctx, account, ledgerIndex, "SignerList", 10)
	if err != nil || len(result.AccountObjects) == 0 {
		return []any{}
	}

	var signerLists []any
	for _, obj := range result.AccountObjects {
		// Decode the raw SLE binary to JSON
		hexData := hex.EncodeToString(obj.Data)
		decoded, err := binarycodec.Decode(hexData)
		if err != nil {
			continue
		}
		decoded["index"] = strings.ToUpper(obj.Index)
		signerLists = append(signerLists, decoded)
	}
	if signerLists == nil {
		return []any{}
	}
	return signerLists
}
