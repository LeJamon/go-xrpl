package conformance

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/amm"
)

// parseDropsAmount parses a JSON amount field (can be string or number) into drops.
func parseDropsAmount(raw json.RawMessage) (uint64, error) {
	if len(raw) == 0 {
		return 0, fmt.Errorf("empty amount")
	}

	// Try as string first (quoted number)
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strconv.ParseUint(s, 10, 64)
	}

	// Try as number
	var n uint64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, nil
	}

	return 0, fmt.Errorf("cannot parse amount: %s", string(raw))
}

// prescanAMMAddresses scans fixture steps to find all issuer addresses
// associated with LP token currencies (03-prefixed 40-char hex). These
// addresses are AMM pseudo-account addresses that may differ between rippled
// and go-xrpl due to different parentHash values. Returns the set of LP token
// issuer addresses, the (issuer, currency) pairs for precise matching,
// the set of all addresses that appear in steps but are NOT funded (potential
// AMM pseudo-account addresses that may not use LP token currencies), and
// the set of unfunded addresses used as the Account field of non-AMMCreate
// transactions (user accounts, not AMM pseudo-accounts).
func prescanAMMAddresses(steps []Step) (map[string]bool, []ammPair, map[string]bool, map[string]bool) {
	addrs := make(map[string]bool)
	var pairs []ammPair

	// Collect all addresses from all steps, and funded addresses separately.
	allAddrs := make(map[string]bool)
	fundedAddrs := make(map[string]bool)
	// Track addresses used as Account of non-AMMCreate transactions.
	nonAMMAccountAddrs := make(map[string]bool)

	// Special addresses that should never be remapped.
	specialAddrs := map[string]bool{
		"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh": true, // genesis/root
		"rrrrrrrrrrrrrrrrrrrrrhoLvTp":        true, // ACCOUNT_ZERO
		"rrrrrrrrrrrrrrrrrrrrBZbvji":         true, // ACCOUNT_ONE / NaN account
	}

	for _, step := range steps {
		// Track funded addresses
		if step.Op == "fund" && step.Address != "" {
			fundedAddrs[step.Address] = true
		}

		// Check tx_json for LP token issuers and all addresses
		if step.TxJSON != nil {
			var txj map[string]any
			if json.Unmarshal(step.TxJSON, &txj) == nil {
				collectLPTokenIssuers(txj, addrs, &pairs)
				collectAllAddresses(txj, allAddrs)
				// Track Account of non-AMMCreate transactions
				if acct, ok := txj["Account"].(string); ok {
					txType, _ := txj["TransactionType"].(string)
					if txType != "AMMCreate" {
						nonAMMAccountAddrs[acct] = true
					}
				}
			}
		}
		// Check trust limit_amount
		if step.LimitAmount != nil {
			if step.LimitAmount.Issuer != "" {
				allAddrs[step.LimitAmount.Issuer] = true
			}
			if isLPTokenCurrency(step.LimitAmount.Currency) {
				if step.LimitAmount.Issuer != "" {
					addrs[step.LimitAmount.Issuer] = true
					pairs = append(pairs, ammPair{issuer: step.LimitAmount.Issuer, currency: step.LimitAmount.Currency})
				}
			}
		}
	}

	// Compute unfunded addresses: addresses that appear in steps but are
	// not funded and not special. These are candidates for AMM pseudo-accounts.
	unfunded := make(map[string]bool)
	for addr := range allAddrs {
		if !fundedAddrs[addr] && !specialAddrs[addr] {
			unfunded[addr] = true
		}
	}

	// Filter nonAMMAccountAddrs to only include unfunded addresses
	nonAMMAcctResult := make(map[string]bool)
	for addr := range nonAMMAccountAddrs {
		if unfunded[addr] {
			nonAMMAcctResult[addr] = true
		}
	}

	return addrs, pairs, unfunded, nonAMMAcctResult
}

// collectAllAddresses recursively walks a JSON map to collect all string
// values that look like XRPL addresses (start with 'r', 25-35 chars).
func collectAllAddresses(obj map[string]any, addrs map[string]bool) {
	for key, v := range obj {
		switch val := v.(type) {
		case string:
			// Only collect addresses from fields that would contain account
			// addresses, not from arbitrary string fields like TxnSignature.
			if isAddressField(key) && isXRPLAddress(val) {
				addrs[val] = true
			}
		case map[string]any:
			collectAllAddresses(val, addrs)
		case []any:
			for _, item := range val {
				if m, ok := item.(map[string]any); ok {
					collectAllAddresses(m, addrs)
				}
			}
		}
	}
}

// isAddressField returns true if the JSON field name typically contains an
// XRPL account address.
func isAddressField(name string) bool {
	switch name {
	case "Account", "Destination", "issuer", "Issuer",
		"Owner", "Authorize", "Unauthorize",
		"RegularKey", "Target":
		return true
	}
	return false
}

// isXRPLAddress returns true if s looks like an XRPL base58 address.
func isXRPLAddress(s string) bool {
	return len(s) >= 25 && len(s) <= 35 && s[0] == 'r'
}

// isLPTokenCurrency returns true if the currency is an LP token currency
// (40-char hex starting with "03").
func isLPTokenCurrency(currency string) bool {
	return len(currency) == 40 && strings.HasPrefix(strings.ToUpper(currency), "03")
}

// collectLPTokenIssuers recursively walks a JSON map to find amount objects
// with LP token currencies and collects their issuer addresses and pairs.
func collectLPTokenIssuers(obj map[string]any, addrs map[string]bool, pairs *[]ammPair) {
	for _, v := range obj {
		switch val := v.(type) {
		case map[string]any:
			// Check if this is an amount object with LP token currency
			if cur, ok := val["currency"].(string); ok && isLPTokenCurrency(cur) {
				if issuer, ok := val["issuer"].(string); ok && issuer != "" {
					addrs[issuer] = true
					*pairs = append(*pairs, ammPair{issuer: issuer, currency: cur})
				}
			}
			// Recurse into nested objects
			collectLPTokenIssuers(val, addrs, pairs)
		case []any:
			for _, item := range val {
				if m, ok := item.(map[string]any); ok {
					collectLPTokenIssuers(m, addrs, pairs)
				}
			}
		}
	}
}

// discoverAMMAddress looks up the AMM entry for the given asset pair in the
// current ledger and returns the actual AMM pseudo-account address.
func (r *runner) discoverAMMAddress(asset1, asset2 tx.Asset) string {
	ammKeylet := amm.ComputeAMMKeylet(asset1, asset2)
	data, err := r.env.Ledger().Read(ammKeylet)
	if err != nil || data == nil {
		return ""
	}

	ammData, err := amm.ParseAMMData(data)
	if err != nil {
		return ""
	}

	addr, err := state.EncodeAccountID(ammData.Account)
	if err != nil {
		return ""
	}
	return addr
}

// registerAMMMapping is called after a successful AMMCreate to build the
// address mapping from fixture AMM addresses to actual go-xrpl AMM addresses.
// It extracts the asset pair from the AMMCreate tx_json, looks up the actual
// AMM account, and maps fixture AMM addresses that were seen with this AMM's
// LP token currency.
//
// If LP token currency matching fails (the AMM address only appears with
// non-LP-token currencies, e.g., as a TrustSet issuer for USD), it falls
// back to matching against unfunded addresses found in fixture steps.
func (r *runner) registerAMMMapping(step Step) {
	// Parse asset pair from tx_json
	if step.TxJSON == nil {
		return
	}
	var txj map[string]any
	if json.Unmarshal(step.TxJSON, &txj) != nil {
		return
	}

	// Extract asset pair from Amount and Amount2
	asset1 := extractAsset(txj, "Amount")
	asset2 := extractAsset(txj, "Amount2")
	if asset1.Currency == "" && asset1.Issuer == "" && asset2.Currency == "" {
		return
	}

	// Discover the actual AMM account address
	actualAddr := r.discoverAMMAddress(asset1, asset2)
	if actualAddr == "" {
		return
	}

	// Phase 1: Try matching by LP token currency (precise matching).
	lptCurrency := strings.ToUpper(amm.GenerateAMMLPTCurrency(asset1.Currency, asset2.Currency))
	matched := false

	for fixtureAddr := range r.fixtureAMMAddrs {
		if _, alreadyMapped := r.ammAddrMap[fixtureAddr]; alreadyMapped {
			continue
		}
		if r.fixtureAddrSeenWithCurrency(fixtureAddr, lptCurrency) {
			r.ammAddrMap[fixtureAddr] = actualAddr
			matched = true
		}
	}

	if matched {
		return
	}

	// Phase 2: Fallback — match against unfunded addresses by proximity.
	// Some fixtures reference the AMM pseudo-account with non-LP-token
	// currencies (e.g., TrustSet issuer for USD, Payment Destination).
	// These addresses won't appear in the LP token prescan.
	//
	// Strategy: find the unfunded, unmapped address that first appears
	// in steps AFTER this AMMCreate step and BEFORE the next scope
	// boundary (env_reset, next fund-after-tx, or next AMMCreate that
	// creates a different AMM). The AMM address is only referenced after
	// the AMMCreate that produces it.
	candidate := r.findUnfundedAMMByProximity(step)
	if candidate != "" {
		r.ammAddrMap[candidate] = actualAddr
		return
	}

	// Last resort: if there's exactly one unfunded unmapped address total
	// that is NOT used as the Account of a non-AMMCreate transaction,
	// it must be this AMM account.
	// Addresses that appear as the Account field of other transaction types
	// (e.g., AMMVote, Payment) are user accounts, not AMM pseudo-accounts.
	var remaining []string
	for addr := range r.fixtureUnfundedAddrs {
		if _, alreadyMapped := r.ammAddrMap[addr]; !alreadyMapped {
			// Exclude addresses that appear as the Account field of
			// non-AMMCreate transactions — those are user accounts.
			if r.fixtureNonAMMAccountAddrs[addr] {
				continue
			}
			remaining = append(remaining, addr)
		}
	}
	if len(remaining) == 1 {
		r.ammAddrMap[remaining[0]] = actualAddr
	}
}

// findUnfundedAMMByProximity finds the unfunded address that first appears
// in fixture steps immediately after the given AMMCreate step. The AMM
// pseudo-account address only appears AFTER the AMMCreate that creates it,
// so the first unfunded address we encounter in the window between this
// AMMCreate and the next scope boundary (env_reset or next AMMCreate) is
// the AMM account.
func (r *runner) findUnfundedAMMByProximity(ammCreateStep Step) string {
	// Find the index of this AMMCreate step in the fixture
	ammCreateIdx := -1
	for i, s := range r.fixtureSteps {
		if s.TxJSON != nil {
			// Match by tx_json and tx_blob content identity
			if string(s.TxJSON) == string(ammCreateStep.TxJSON) &&
				s.TxBlob == ammCreateStep.TxBlob {
				ammCreateIdx = i
				break
			}
		}
	}
	if ammCreateIdx < 0 {
		return ""
	}

	// Scan steps after the AMMCreate for unfunded addresses.
	// Stop at the next scope boundary: env_reset, or the first fund step
	// that comes after tx steps (implicit scope reset).
	for i := ammCreateIdx + 1; i < len(r.fixtureSteps); i++ {
		s := r.fixtureSteps[i]

		// Stop at scope boundaries
		if s.Op == "env_reset" {
			break
		}

		// Check tx_json for unfunded addresses
		if s.TxJSON != nil {
			var txj map[string]any
			if json.Unmarshal(s.TxJSON, &txj) == nil {
				addr := r.findFirstUnfundedAddr(txj)
				if addr != "" {
					return addr
				}
			}
		}

		// Check trust limit_amount
		if s.LimitAmount != nil && s.LimitAmount.Issuer != "" {
			addr := s.LimitAmount.Issuer
			if r.fixtureUnfundedAddrs[addr] {
				if _, alreadyMapped := r.ammAddrMap[addr]; !alreadyMapped {
					return addr
				}
			}
		}
	}

	return ""
}

// findFirstUnfundedAddr looks through a tx_json for the first address that
// is unfunded and unmapped. It checks Destination and issuer fields.
func (r *runner) findFirstUnfundedAddr(txj map[string]any) string {
	// Check Destination first (most common for Payment to AMM)
	if dest, ok := txj["Destination"].(string); ok {
		if r.fixtureUnfundedAddrs[dest] {
			if _, alreadyMapped := r.ammAddrMap[dest]; !alreadyMapped {
				return dest
			}
		}
	}

	// Check issuers in amount objects
	for _, field := range []string{"Amount", "LimitAmount", "SendMax", "DeliverMin"} {
		if amt, ok := txj[field].(map[string]any); ok {
			if issuer, ok := amt["issuer"].(string); ok && issuer != "" {
				if r.fixtureUnfundedAddrs[issuer] {
					if _, alreadyMapped := r.ammAddrMap[issuer]; !alreadyMapped {
						return issuer
					}
				}
			}
		}
	}

	return ""
}

// fixtureAddrSeenWithCurrency checks if a fixture address was seen as the
// issuer of the given LP token currency in the prescan data.
func (r *runner) fixtureAddrSeenWithCurrency(fixtureAddr, lptCurrency string) bool {
	for _, pair := range r.fixtureAMMPairs {
		if pair.issuer == fixtureAddr && strings.EqualFold(pair.currency, lptCurrency) {
			return true
		}
	}
	return false
}

// extractAsset extracts a tx.Asset from a JSON amount field.
func extractAsset(txj map[string]any, field string) tx.Asset {
	val, ok := txj[field]
	if !ok {
		return tx.Asset{}
	}

	switch v := val.(type) {
	case map[string]any:
		// IOU amount: {currency, issuer, value}
		asset := tx.Asset{}
		if cur, ok := v["currency"].(string); ok {
			asset.Currency = cur
		}
		if iss, ok := v["issuer"].(string); ok {
			asset.Issuer = iss
		}
		return asset
	case string:
		// XRP amount (drops string)
		return tx.Asset{Currency: "XRP"}
	case float64:
		// XRP amount (drops number)
		return tx.Asset{Currency: "XRP"}
	}
	return tx.Asset{}
}

// remapAMMAddresses remaps AMM pseudo-account addresses in a parsed
// transaction. It walks all Amount and Asset fields using reflection and
// replaces issuer addresses that match fixture AMM addresses with the actual
// go-xrpl AMM addresses.
func (r *runner) remapAMMAddresses(txn tx.Transaction) {
	if len(r.ammAddrMap) == 0 {
		return
	}
	remapAmountFields(reflect.ValueOf(txn), r.ammAddrMap)
}

// remapAmountFields recursively walks a reflect.Value to find and remap
// Amount.Issuer, Asset.Issuer, and address string fields (Destination, etc.).
func remapAmountFields(v reflect.Value, addrMap map[string]string) {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return
		}
		remapAmountFields(v.Elem(), addrMap)
	case reflect.Struct:
		t := v.Type()

		// Check if this is a state.Amount or Asset (has Issuer and Currency fields)
		issuerField := v.FieldByName("Issuer")
		currencyField := v.FieldByName("Currency")
		if issuerField.IsValid() && issuerField.CanSet() && issuerField.Kind() == reflect.String &&
			currencyField.IsValid() && currencyField.Kind() == reflect.String {
			issuer := issuerField.String()
			if actual, ok := addrMap[issuer]; ok {
				issuerField.SetString(actual)
			}
		}

		// Also check string fields that may contain AMM addresses.
		// Common fields: Destination, Account (in inner tx contexts), etc.
		for i := 0; i < t.NumField(); i++ {
			field := v.Field(i)
			if !field.CanInterface() {
				continue // skip unexported fields
			}

			// Remap string fields that match known AMM addresses
			if field.Kind() == reflect.String && field.CanSet() {
				s := field.String()
				if actual, ok := addrMap[s]; ok {
					field.SetString(actual)
				}
			}

			// Recurse into struct/ptr/slice fields
			remapAmountFields(field, addrMap)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			remapAmountFields(v.Index(i), addrMap)
		}
	}
}

// accountByAddress looks up a test account by address in the runner's
// account map. If no registered account matches, creates a temporary
// reference so the caller can interact with the ledger.
func (r *runner) accountByAddress(address string) *jtx.Account {
	for _, acc := range r.accounts {
		if acc.Address == address {
			return acc
		}
	}
	return jtx.NewAccountWithAddress("tmp_"+address[len(address)-8:], address)
}
