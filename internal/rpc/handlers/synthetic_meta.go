package handlers

import (
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/LeJamon/go-xrpl/keylet"
)

// metadataToMap renders simulation metadata as a mutable JSON object so
// synthetic fields can be injected. The production adapter supplies a typed
// *tx.Metadata (marshalled here via its MarshalJSON); a value that is already a
// map is returned as-is. Returns nil when the metadata cannot be rendered.
func metadataToMap(meta any) map[string]any {
	if m, ok := meta.(map[string]any); ok {
		return m
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m
}

// enrichSimulateMeta injects rippled's synthetic transaction-metadata fields
// (delivered_amount, nftoken_id / nftoken_ids, offer_id, mpt_issuance_id) into
// a simulated transaction's `meta` JSON, matching what tx / account_tx report
// for validated transactions. All of these require tesSUCCESS and are computed
// from the metadata's AffectedNodes, so a failed simulation is a no-op.
// Reference: rippled Simulate.cpp:277-288 (insertDeliveredAmount /
// insertNFTSyntheticInJson / insertMPTokenIssuanceID).
func enrichSimulateMeta(meta, txJSON map[string]any) {
	if meta == nil {
		return
	}
	if result, _ := meta["TransactionResult"].(string); result != "tesSUCCESS" {
		return
	}
	txType, _ := txJSON["TransactionType"].(string)

	insertSimulateDeliveredAmount(meta, txJSON, txType)
	insertNFTSynthetic(meta, txJSON, txType)
	insertMPTokenIssuanceID(meta, txJSON, txType)
}

func enrichTransactionMeta(meta, txJSON map[string]any) {
	if meta == nil {
		return
	}
	source := txJSON
	if _, hasAmount := source["Amount"]; !hasAmount {
		if deliverMax, ok := source["DeliverMax"]; ok {
			source = make(map[string]any, len(txJSON)+1)
			for key, value := range txJSON {
				source[key] = value
			}
			source["Amount"] = deliverMax
		}
	}
	InjectDeliveredAmount(source, meta)
	txType, _ := source["TransactionType"].(string)
	insertNFTSynthetic(meta, source, txType)
	insertMPTokenIssuanceID(meta, source, txType)
}

// insertSimulateDeliveredAmount mirrors rippled insertDeliveredAmount /
// getDeliveredAmount for a simulated (current open ledger) transaction. Only
// Payment / CheckCash / AccountDelete carry a delivered amount. If the engine
// already recorded one it is kept; otherwise the transaction's Amount is used
// (a simulation always runs against a recent-enough ledger for the fix1623
// fallback), falling back to "unavailable" when there is no Amount.
func insertSimulateDeliveredAmount(meta, txJSON map[string]any, txType string) {
	switch txType {
	case "Payment", "CheckCash", "AccountDelete":
	default:
		return
	}
	if _, ok := meta["delivered_amount"]; ok {
		return
	}
	if amount, ok := txJSON["Amount"]; ok {
		meta["delivered_amount"] = amount
	} else {
		meta["delivered_amount"] = "unavailable"
	}
}

// insertNFTSynthetic mirrors rippled insertNFTSyntheticInJson: nftoken_id /
// nftoken_ids for mint / accept-offer / cancel-offer, and offer_id for a
// create-offer (or a mint that also lists the token via an Amount).
func insertNFTSynthetic(meta, txJSON map[string]any, txType string) {
	nodes := affectedNodes(meta)

	switch txType {
	case "NFTokenMint":
		if id, ok := nftokenIDFromPage(nodes); ok {
			meta["nftoken_id"] = id
		}
	case "NFTokenAcceptOffer":
		if ids := nftokenIDsFromDeletedOffers(nodes); len(ids) > 0 {
			meta["nftoken_id"] = ids[0]
		}
	case "NFTokenCancelOffer":
		ids := nftokenIDsFromDeletedOffers(nodes)
		arr := make([]any, len(ids))
		for i, id := range ids {
			arr[i] = id
		}
		meta["nftoken_ids"] = arr
	}

	if txType == "NFTokenCreateOffer" || (txType == "NFTokenMint" && hasKey(txJSON, "Amount")) {
		if id, ok := offerIDFromCreatedOffer(nodes); ok {
			meta["offer_id"] = id
		}
	}
}

// insertMPTokenIssuanceID mirrors rippled insertMPTokenIssuanceID: the id of
// the MPTokenIssuance created by an MPTokenIssuanceCreate, derived from the
// created issuance's Sequence and Issuer (makeMptID).
func insertMPTokenIssuanceID(meta, txJSON map[string]any, txType string) {
	if txType != "MPTokenIssuanceCreate" {
		return
	}
	for _, n := range affectedNodes(meta) {
		action, inner := nodeParts(n)
		if action != "CreatedNode" || nodeType(inner) != "MPTokenIssuance" {
			continue
		}
		nf, _ := inner["NewFields"].(map[string]any)
		seq, ok := jsonUint32(nf["Sequence"])
		if !ok {
			continue
		}
		issuer, _ := nf["Issuer"].(string)
		accountID, err := decodeAccountID(issuer)
		if err != nil {
			continue
		}
		mptID := keylet.MakeMPTID(seq, accountID)
		meta["mpt_issuance_id"] = strings.ToUpper(hex.EncodeToString(mptID[:]))
		return
	}
}

// nftokenIDFromPage extracts the newly minted NFTokenID by diffing the final
// against the previous NFTokenIDs across all affected NFTokenPage nodes
// (rippled getNFTokenIDFromPage). The added token is the single element present
// in the final set but not the previous one.
func nftokenIDFromPage(nodes []any) (string, bool) {
	var prevIDs, finalIDs []string
	for _, n := range nodes {
		action, inner := nodeParts(n)
		if nodeType(inner) != "NFTokenPage" {
			continue
		}
		switch action {
		case "CreatedNode":
			nf, _ := inner["NewFields"].(map[string]any)
			finalIDs = append(finalIDs, nftokenIDsInFields(nf)...)
		case "ModifiedNode":
			prev, _ := inner["PreviousFields"].(map[string]any)
			// A page whose NFTokens did not change (only page-link fields did)
			// carries no NFTokens in PreviousFields and must be skipped.
			if _, has := prev["NFTokens"]; !has {
				continue
			}
			prevIDs = append(prevIDs, nftokenIDsInFields(prev)...)
			fin, _ := inner["FinalFields"].(map[string]any)
			finalIDs = append(finalIDs, nftokenIDsInFields(fin)...)
		}
	}

	// NFTs are added one at a time, so the final set is exactly one longer.
	if len(finalIDs) != len(prevIDs)+1 {
		return "", false
	}
	for i := range finalIDs {
		if i >= len(prevIDs) || finalIDs[i] != prevIDs[i] {
			return finalIDs[i], true
		}
	}
	return "", false
}

// nftokenIDsFromDeletedOffers collects the NFTokenIDs of every deleted
// NFTokenOffer, sorted and de-duplicated (rippled getNFTokenIDFromDeletedOffer).
func nftokenIDsFromDeletedOffers(nodes []any) []string {
	var ids []string
	for _, n := range nodes {
		action, inner := nodeParts(n)
		if action != "DeletedNode" || nodeType(inner) != "NFTokenOffer" {
			continue
		}
		ff, _ := inner["FinalFields"].(map[string]any)
		if id, _ := ff["NFTokenID"].(string); id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return dedupSorted(ids)
}

// offerIDFromCreatedOffer returns the ledger index of the first created
// NFTokenOffer (rippled getOfferIDFromCreatedOffer).
func offerIDFromCreatedOffer(nodes []any) (string, bool) {
	for _, n := range nodes {
		action, inner := nodeParts(n)
		if action != "CreatedNode" || nodeType(inner) != "NFTokenOffer" {
			continue
		}
		if id, _ := inner["LedgerIndex"].(string); id != "" {
			return id, true
		}
	}
	return "", false
}

// affectedNodes returns the AffectedNodes array of a metadata JSON map.
func affectedNodes(meta map[string]any) []any {
	nodes, _ := meta["AffectedNodes"].([]any)
	return nodes
}

// nodeParts returns the node action (CreatedNode / ModifiedNode / DeletedNode)
// and its inner object for one AffectedNodes element.
func nodeParts(n any) (string, map[string]any) {
	m, ok := n.(map[string]any)
	if !ok {
		return "", nil
	}
	for _, action := range [...]string{"CreatedNode", "ModifiedNode", "DeletedNode"} {
		if inner, ok := m[action].(map[string]any); ok {
			return action, inner
		}
	}
	return "", nil
}

// nodeType returns an affected node's LedgerEntryType.
func nodeType(inner map[string]any) string {
	t, _ := inner["LedgerEntryType"].(string)
	return t
}

// nftokenIDsInFields extracts the NFTokenIDs from a fields object's NFTokens
// array (each element is {"NFToken": {"NFTokenID": ...}}).
func nftokenIDsInFields(fields map[string]any) []string {
	arr, ok := fields["NFTokens"].([]any)
	if !ok {
		return nil
	}
	ids := make([]string, 0, len(arr))
	for _, el := range arr {
		wrapper, ok := el.(map[string]any)
		if !ok {
			continue
		}
		token, ok := wrapper["NFToken"].(map[string]any)
		if !ok {
			continue
		}
		if id, _ := token["NFTokenID"].(string); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// jsonUint32 coerces a JSON-decoded numeric value to a uint32.
func jsonUint32(v any) (uint32, bool) {
	switch n := v.(type) {
	case float64:
		if n >= 0 && n <= 4294967295 {
			return uint32(n), true
		}
	case uint32:
		return n, true
	case json.Number:
		if i, err := n.Int64(); err == nil && i >= 0 && i <= 4294967295 {
			return uint32(i), true
		}
	}
	return 0, false
}

// hasKey reports whether a JSON object carries a non-null value for key.
func hasKey(m map[string]any, key string) bool {
	v, ok := m[key]
	return ok && v != nil
}

// dedupSorted removes adjacent duplicates from a sorted slice in place.
func dedupSorted(s []string) []string {
	if len(s) < 2 {
		return s
	}
	out := s[:1]
	for _, v := range s[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}
