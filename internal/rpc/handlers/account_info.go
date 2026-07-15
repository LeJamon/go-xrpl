package handlers

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
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
	lsfAllowTrustLineLocking    = entry.LsfAllowTrustLineLocking
	lsfAllowTrustLineClawback   = entry.LsfAllowTrustLineClawback
)

// AccountInfoMethod handles the account_info RPC method.
type AccountInfoMethod struct{ BaseHandler }

func (m *AccountInfoMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	rawFields, fieldsErr := rawJSONFields(params)
	if fieldsErr != nil {
		return nil, fieldsErr
	}

	account := ""
	if accountRaw, ok := rawFields["account"]; ok {
		var valid bool
		account, valid = rawJSONString(accountRaw)
		if !valid {
			return nil, types.RPCErrorInvalidField("account")
		}
	} else if identRaw, ok := rawFields["ident"]; ok {
		var valid bool
		account, valid = rawJSONString(identRaw)
		if !valid {
			return nil, types.RPCErrorInvalidField("ident")
		}
	} else {
		return nil, types.RPCErrorMissingField("account")
	}

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	ledgerIndex, selErr := resolveLedgerSelector(params)
	if selErr != nil {
		return nil, selErr
	}
	ledger, lookupValidated, lookupErr := LookupLedger(ctx, params)
	if lookupErr != nil {
		return nil, lookupErr
	}
	ledgerIndex = strconv.FormatUint(uint64(ledger.Sequence()), 10)
	lookupFields := ledgerEntryResponseFields(ledger, lookupValidated)
	_, accountID, decodeErr := addresscodec.DecodeClassicAddressToAccountID(account)
	if decodeErr != nil {
		rpcErr := types.RPCErrorActMalformed("Account malformed.")
		rpcErr.Extra = lookupFields
		return nil, rpcErr
	}
	canonicalAccount, encodeErr := addresscodec.EncodeAccountIDToClassicAddress(accountID)
	if encodeErr != nil {
		rpcErr := types.RPCErrorActMalformed("Account malformed.")
		rpcErr.Extra = lookupFields
		return nil, rpcErr
	}

	info, err := ctx.Services.Ledger.GetAccountInfo(ctx.Context, canonicalAccount, ledgerIndex)
	if err != nil {
		if errors.Is(err, svcerr.ErrAccountNotFound) {
			lookupFields["account"] = canonicalAccount
			rpcErr := types.RPCErrorActNotFound("Account not found.")
			rpcErr.Extra = lookupFields
			return nil, rpcErr
		}
		if errors.Is(err, svcerr.ErrLedgerNotFound) {
			return nil, types.RPCErrorLgrNotFound("Ledger not found.")
		}
		return nil, mapAccountQueryErr(err, fmt.Sprintf("Failed to get account info: %v", err))
	}

	queue := false
	if queueRaw, ok := rawFields["queue"]; ok {
		queue = jsonCppBoolRaw(queueRaw)
	}
	if queue && ledger.IsClosed() {
		rpcErr := types.RPCErrorInvalidParams("Invalid parameters.")
		rpcErr.Extra = lookupFields
		return nil, rpcErr
	}

	signerLists := false
	if signerListsRaw, ok := rawFields["signer_lists"]; ok {
		signerLists = jsonCppBoolRaw(signerListsRaw)
	}

	// Build account_data by decoding the full SLE binary via binarycodec,
	// matching rippled's injectSLE which serializes all fields from the SLE.
	accountData := m.buildAccountData(info)
	if emailHash, ok := accountData["EmailHash"].(string); ok {
		accountData["urlgravatar"] = "https://www.gravatar.com/avatar/" + strings.ToLower(emailHash)
	}

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
	accountFlags["disallowIncomingNFTokenOffer"] = flags&lsfDisallowIncomingNFTOffer != 0
	accountFlags["disallowIncomingCheck"] = flags&lsfDisallowIncomingCheck != 0
	accountFlags["disallowIncomingPayChan"] = flags&lsfDisallowIncomingPayChan != 0
	accountFlags["disallowIncomingTrustline"] = flags&lsfDisallowIncomingTrustln != 0

	rules := amendment.EmptyRules()
	if source, ok := ledger.(types.LedgerAmendmentRulesSource); ok {
		if ledgerRules := source.LedgerAmendmentRules(); ledgerRules != nil {
			rules = ledgerRules
		}
	}
	if rules.Enabled(amendment.FeatureClawback) {
		accountFlags["allowTrustLineClawback"] = flags&lsfAllowTrustLineClawback != 0
	}
	if rules.Enabled(amendment.FeatureTokenEscrow) {
		accountFlags["allowTrustLineLocking"] = flags&lsfAllowTrustLineLocking != 0
	}
	if info.Index != "" {
		accountData["index"] = strings.ToUpper(info.Index)
	}

	response := map[string]any{
		"account_data":  accountData,
		"account_flags": accountFlags,
	}
	addPseudoAccount(response, accountData)
	for key, value := range lookupFields {
		response[key] = value
	}

	if signerListsRaw, ok := rawFields["signer_lists"]; ctx.ApiVersion > 1 && ok {
		if _, valid := rawJSONBool(signerListsRaw); !valid {
			rpcErr := types.RPCErrorInvalidParams("Invalid parameters.")
			rpcErr.Extra = response
			return nil, rpcErr
		}
	}

	if queue {
		response["queue_data"] = buildAccountQueueData(ctx.Services, canonicalAccount)
	}

	// Load signer lists if requested
	if signerLists {
		signerListEntries := m.loadSignerLists(ctx.Context, ctx.Services, canonicalAccount, ledgerIndex)
		if ctx.ApiVersion > 1 {
			// API v2: signer_lists at top level
			response["signer_lists"] = signerListEntries
		} else {
			// API v1: nested under account_data
			accountData["signer_lists"] = signerListEntries
		}
	}

	return response, nil
}

// pseudoAccountFields are the AccountRoot designator fields, in the SOTemplate
// order rippled iterates (getPseudoAccountFields). A pseudo-account carries
// exactly one; the RPC reports its type by stripping the trailing "ID".
var pseudoAccountFields = [...]string{"AMMID", "VaultID", "LoanBrokerID"}

// addPseudoAccount sets response["pseudo_account"] = {"type": <name>} when the
// account root carries a pseudo-account designator, matching rippled
// doAccountInfo. The designator's own hash stays in account_data; only the
// derived type name (field name minus "ID") is surfaced here.
func addPseudoAccount(response, accountData map[string]any) {
	for _, field := range pseudoAccountFields {
		if _, present := accountData[field]; present {
			response["pseudo_account"] = map[string]any{"type": strings.TrimSuffix(field, "ID")}
			return
		}
	}
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
	result, err := services.Ledger.GetAccountObjects(ctx, account, ledgerIndex, "SignerList", 10, "")
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
