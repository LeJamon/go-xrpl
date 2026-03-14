package v1

// FormatTransactionEntry transforms a v2 transaction entry to v1 format.
// v1 differences:
//   - "tx_json" key becomes "tx"
//   - "meta_blob" key becomes "meta" (binary mode)
//   - "date" field added inside "tx" from ledger close time
//   - No top-level "ledger_hash", "close_time_iso", "ctid" per entry
//   - "inLedger" field present (deprecated alias for ledger_index)
//
// TODO: Implement when v1 support is needed.
func FormatTransactionEntry(v2Entry map[string]interface{}) map[string]interface{} {
	return v2Entry // passthrough stub
}

// FormatAccountInfo transforms a v2 account_info response to v1 format.
// v1 differences:
//   - signer_lists nested under account_data (not top-level)
//   - Non-bool signer_lists parameter accepted without error
//
// TODO: Implement when v1 support is needed.
func FormatAccountInfo(v2Response map[string]interface{}) map[string]interface{} {
	return v2Response // passthrough stub
}

// FormatSubmitResponse transforms a v2 submit response to v1 format.
// v1 differences:
//   - "tx_json" key becomes "tx"
//
// TODO: Implement when v1 support is needed.
func FormatSubmitResponse(v2Response map[string]interface{}) map[string]interface{} {
	return v2Response // passthrough stub
}

// FormatLedgerResponse transforms a v2 ledger response to v1 format.
// v1 differences:
//   - Expanded transactions use "tx"/"meta" instead of "tx_json"/"meta_blob"
//
// TODO: Implement when v1 support is needed.
func FormatLedgerResponse(v2Response map[string]interface{}) map[string]interface{} {
	return v2Response // passthrough stub
}
