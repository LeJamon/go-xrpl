// Package v1 provides API v1 response formatters for RPC handlers.
//
// The main handlers (parent package) implement API v2 response format,
// which is the current rippled default. This package provides optional
// v1 response transformation for backward compatibility with older nodes.
//
// API v1 differences from v2 include:
//   - Transaction responses use "tx" key instead of "tx_json"
//   - Binary metadata uses "meta" key instead of "meta_blob"
//   - signer_lists is nested under account_data (not top-level)
//   - Includes deprecated fields like "inLedger", "date" inside tx
//   - Missing per-entry fields: close_time_iso, ledger_hash, ctid
//   - Less strict parameter type validation (no bool enforcement)
//   - Different error codes for some edge cases
//
// STATUS: Not implemented. Stub files are provided as scaffolding.
// The v2 handlers are the priority. V1 support can be added later
// if needed for backward compatibility with older XRPL nodes.
package v1
