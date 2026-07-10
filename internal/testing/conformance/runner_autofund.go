package conformance

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/amm"
)

// shouldAutoFund returns true if the fixture needs implicit account funding.
// This is true when at least one tx step expects an applied result (tesSUCCESS
// or tec*) and its Account is not established by a preceding fund step.
// Many rippled test fixtures depend on accounts existing from prior test context
// (accounts funded before the test case captured in the fixture). When fund
// steps exist but only AFTER the first applied tx, we still need auto-funding
// for accounts that send those early transactions.
func (r *runner) shouldAutoFund(steps []Step) bool {
	masterAddr := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	// Collect addresses funded by explicit fund steps OR by inline Payment
	// transactions from master (which create the destination account).
	fundedAt := make(map[string]int) // address -> step index
	for i, s := range steps {
		if s.Op == "fund" && s.Address != "" {
			if _, exists := fundedAt[s.Address]; !exists {
				fundedAt[s.Address] = i
			}
		}
		// Also treat Payment from master to an address as implicit funding.
		// In rippled test helpers like runTx(), accounts are created by
		// Payments from master rather than explicit fund() calls.
		if s.Op == "tx" && s.TxJSON != nil {
			var txj map[string]any
			if json.Unmarshal(s.TxJSON, &txj) == nil {
				if txj["TransactionType"] == "Payment" &&
					txj["Account"] == masterAddr {
					if dest, ok := txj["Destination"].(string); ok && dest != "" {
						if _, exists := fundedAt[dest]; !exists {
							fundedAt[dest] = i
						}
					}
				}
			}
		}
	}

	// Check if any tx step expects an applied result from an account that
	// isn't funded by a preceding fund step or inline Payment.
	for i, s := range steps {
		if s.Op != "tx" || s.TxJSON == nil {
			continue
		}
		if s.ExpectTER != "tesSUCCESS" && !strings.HasPrefix(s.ExpectTER, "tec") {
			continue
		}

		var txj map[string]any
		if err := json.Unmarshal(s.TxJSON, &txj); err != nil {
			continue
		}
		addr, ok := txj["Account"].(string)
		if !ok || addr == "" || addr == masterAddr {
			continue
		}

		// Check if this account is funded BEFORE this tx step
		fundIdx, funded := fundedAt[addr]
		if !funded || fundIdx > i {
			return true
		}
	}
	return false
}

// autoFundAccounts scans tx_json steps for accounts and funds them so their
// sequences match the fixture's expectations. Accounts are grouped by their
// first expected sequence: accounts with the same initial seq are funded in
// the same ledger, with closes between groups to increment open_ledger_seq.
//
// For Credential and similar transaction types, auxiliary accounts (Subject,
// Issuer, Destination) are also funded when they need to exist for preclaim
// checks. Auxiliary accounts a fixture deliberately leaves uncreated — detected
// via an expected tecNO_TARGET/tecNO_ISSUER on the first tx that references them
// (see findSkipAuxAddresses) — are excluded from auto-funding.
//
// Initial funding amounts are derived from the first post_state entry for
// each account when possible. This is critical for reserve-sensitive tests
// where the exact balance determines the TER code.
func (r *runner) autoFundAccounts(steps []Step) {
	// Derive the initial funding amount for each account from the first
	// post_state entry. For applied tx results (tesSUCCESS/tec*), the
	// post_state balance = initial_balance - fees_consumed. By analyzing
	// how many txs the account has sent before the post_state check, we
	// can infer the initial balance.
	//
	// For simplicity, we use the first post_state balance + (number of
	// fees consumed by this account up to that post_state step) * baseFee.
	// This gives us the initial balance the fixture expects.
	initialBalances := r.deriveInitialBalances(steps)

	// Collect unique account addresses and their first sequence from tx_json.
	type acctInfo struct {
		address  string
		firstSeq uint32
	}
	seen := make(map[string]bool)
	var accounts []acctInfo

	// Also collect auxiliary addresses (Subject, Issuer, Destination) that
	// need to exist but aren't tx senders. These get sequence 0 (no txs from them).
	auxSeen := make(map[string]bool)

	for _, s := range steps {
		if s.Op != "tx" || s.TxJSON == nil {
			continue
		}
		var txj map[string]any
		if err := json.Unmarshal(s.TxJSON, &txj); err != nil {
			continue
		}

		// Collect the Account field (the sender/signer).
		addr, ok := txj["Account"].(string)
		if ok && addr != "" && !seen[addr] {
			// Skip master/genesis account — already exists
			if addr == "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh" {
				seen[addr] = true
			} else {
				seen[addr] = true

				seq := uint32(0)
				if seqF, ok := txj["Sequence"].(float64); ok {
					seq = uint32(seqF)
				}
				accounts = append(accounts, acctInfo{address: addr, firstSeq: seq})
			}
		}

		// Collect auxiliary accounts (Subject, Issuer, Destination).
		// These accounts need to exist for preclaim checks to work correctly,
		// BUT only if they don't have explicit fund steps (which control timing).
		for _, field := range []string{"Subject", "Issuer", "Destination"} {
			if auxAddr, ok := txj[field].(string); ok && auxAddr != "" {
				auxSeen[auxAddr] = true
			}
		}
	}

	// Also collect addresses from post_state — if the fixture expects
	// specific balances for named accounts, those accounts must exist.
	for _, s := range steps {
		if s.PostState == nil {
			continue
		}
		for _, as := range s.PostState.Accounts {
			if as.Address != "" {
				auxSeen[as.Address] = true
			}
		}
	}

	// Determine minimum first_seq from sender accounts.
	minSeq := uint32(0xFFFFFFFF)
	for _, a := range accounts {
		if a.firstSeq > 0 && a.firstSeq < minSeq {
			minSeq = a.firstSeq
		}
	}
	if minSeq == 0xFFFFFFFF {
		minSeq = 4 // Default: funded in open ledger 3, AccountSet → seq 4
	}

	// Determine which auxiliary addresses should NOT be funded because the
	// fixture expects them to not exist (e.g., tecNO_TARGET, tecNO_ISSUER).
	skipAuxAddrs := r.findSkipAuxAddresses(steps)

	// Add auxiliary accounts that aren't already senders and aren't in the
	// skip set. Accounts are skipped if they have explicit fund steps AND the
	// first tx referencing them expects a TER code that depends on them not
	// existing (tecNO_TARGET for Subject/Destination, tecNO_ISSUER for Issuer).
	for auxAddr := range auxSeen {
		if seen[auxAddr] {
			continue
		}
		if auxAddr == "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh" {
			continue
		}
		// Check if it's the zero account
		if auxAddr == "rrrrrrrrrrrrrrrrrrrrrhoLvTp" || auxAddr == "rrrrrrrrrrrrrrrrrrrrBZbvji" {
			continue
		}
		// Skip auxiliary accounts that should not exist for the test to work
		if skipAuxAddrs[auxAddr] {
			continue
		}
		seen[auxAddr] = true
		accounts = append(accounts, acctInfo{address: auxAddr, firstSeq: 0})
	}

	if len(accounts) == 0 {
		return
	}

	// Assign zero-seq accounts to earliest group
	for i := range accounts {
		if accounts[i].firstSeq == 0 {
			accounts[i].firstSeq = minSeq
		}
	}

	// Sort by firstSeq to fund in correct ledger order
	sort.Slice(accounts, func(i, j int) bool {
		return accounts[i].firstSeq < accounts[j].firstSeq
	})

	// Fund accounts grouped by firstSeq.
	// After setupEnv, open_ledger_seq = 3. Account creation sets
	// account.Sequence = open_ledger_seq. FundAmount then does AccountSet
	// (DefaultRipple) which bumps seq by 1. So to get firstSeq = N,
	// we need open_ledger_seq = N - 1 when funding.
	//
	// Starting open_ledger_seq = 3. We close ledgers as needed to reach
	// the target open_ledger_seq for each group.
	currentOpenSeq := r.env.LedgerSeq() // Should be 3 after setupEnv
	for _, a := range accounts {
		targetOpenSeq := a.firstSeq - 1 // open_ledger_seq needed for this account
		for currentOpenSeq < targetOpenSeq {
			r.env.Close()
			currentOpenSeq++
		}

		// Generate a short name from the address (last 8 chars)
		name := a.address
		if len(name) > 8 {
			name = name[len(name)-8:]
		}
		acc := jtx.NewAccountWithAddress(name, a.address)
		r.accounts[name] = acc
		// Also register by full address for post_state lookups
		r.accounts[a.address] = acc

		// Use the derived initial balance if available, otherwise default to 5000 XRP.
		fundAmount := uint64(5_000_000_000)
		if derived, ok := initialBalances[a.address]; ok && derived > 0 {
			fundAmount = derived
		}
		// Bypass TxQ for auto-fund (setup operation, like rippled's apply())
		r.env.SetBypassTxQ(true)
		r.env.FundAmount(acc, fundAmount)
		r.env.SetBypassTxQ(false)
	}

	// Close after all funding so state is committed
	r.env.Close()
}

// findSkipAuxAddresses identifies auxiliary addresses (Subject, Issuer, Destination)
// that should NOT be auto-funded because a tx step expects a TER code that depends
// on the account not existing. For example:
// - tecNO_TARGET: the Subject/Destination doesn't exist
// - tecNO_ISSUER: the Issuer doesn't exist
//
// Only addresses that also have explicit fund steps are considered for skipping,
// because if there's no fund step, the auxiliary account was never meant to be
// created by the fixture at all.
func (r *runner) findSkipAuxAddresses(steps []Step) map[string]bool {
	skipAddrs := make(map[string]bool)

	// Build set of addresses with explicit fund steps
	explicitFundAddrs := make(map[string]bool)
	for _, s := range steps {
		if s.Op == "fund" && s.Address != "" {
			explicitFundAddrs[s.Address] = true
		}
	}

	for _, s := range steps {
		if s.Op != "tx" || s.TxJSON == nil {
			continue
		}
		var txj map[string]any
		if err := json.Unmarshal(s.TxJSON, &txj); err != nil {
			continue
		}

		// tecNO_TARGET: Subject or Destination should not exist
		if s.ExpectTER == "tecNO_TARGET" {
			for _, field := range []string{"Subject", "Destination"} {
				if addr, ok := txj[field].(string); ok && addr != "" && explicitFundAddrs[addr] {
					skipAddrs[addr] = true
				}
			}
		}

		// tecNO_ISSUER: Issuer should not exist
		if s.ExpectTER == "tecNO_ISSUER" {
			if addr, ok := txj["Issuer"].(string); ok && addr != "" && explicitFundAddrs[addr] {
				skipAddrs[addr] = true
			}
		}
	}

	return skipAddrs
}

// deriveInitialBalances infers the initial funding balance for each account
// from the fixture's first post_state entry. For each account, it finds the
// first post_state appearance and adds back the fees that were consumed by
// transactions from that account up to that point.
//
// Example: if account A sends 2 txs (each costing 10 drops) before the first
// post_state shows balance 4999999980, then initial balance = 4999999980 + 20
// = 5000000000 (5B).
func (r *runner) deriveInitialBalances(steps []Step) map[string]uint64 {
	result := make(map[string]uint64)

	// Track how many fees each address has consumed (as tx sender).
	// Only count applied results (tesSUCCESS, tec*) since tem/tef/tel/ter
	// don't deduct fees.
	feesByAddr := make(map[string]uint64)  // address -> total fees paid
	postStateSeen := make(map[string]bool) // already derived for this address

	for _, s := range steps {
		if s.Op == "tx" && s.TxJSON != nil {
			// Count fees for applied results
			if s.ExpectTER == "tesSUCCESS" || strings.HasPrefix(s.ExpectTER, "tec") {
				var txj map[string]any
				if err := json.Unmarshal(s.TxJSON, &txj); err == nil {
					if addr, ok := txj["Account"].(string); ok && addr != "" {
						fee := uint64(10) // default base fee
						if feeStr, ok := txj["Fee"].(string); ok {
							if f, err := strconv.ParseUint(feeStr, 10, 64); err == nil {
								fee = f
							}
						}
						feesByAddr[addr] += fee
					}
				}
			}
		}

		// When we hit a post_state, derive balances for accounts we haven't seen yet.
		if s.PostState != nil {
			for _, as := range s.PostState.Accounts {
				if as.Address == "" || postStateSeen[as.Address] {
					continue
				}
				if as.Address == "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh" {
					continue // skip master
				}
				postStateSeen[as.Address] = true

				balance, err := strconv.ParseUint(as.XRPBalance, 10, 64)
				if err != nil {
					continue
				}

				// Initial balance = post_state balance + fees consumed by this account
				fees := feesByAddr[as.Address]
				result[as.Address] = balance + fees
			}
		}
	}

	return result
}

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
