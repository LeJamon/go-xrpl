// Package txprojection contains the API-version-specific transaction JSON
// shaping shared by RPC handlers and WebSocket publishers.
package txprojection

import (
	"bytes"
	"encoding/json"
	"maps"
	"strings"
)

// ProjectJSON returns a projected copy of a transaction JSON object. The
// source map and all values reachable through it are left untouched.
func ProjectJSON(txJSON map[string]any, hash string, apiVersion int) map[string]any {
	projected := make(map[string]any, len(txJSON)+1)
	maps.Copy(projected, txJSON)
	InjectDeliverMax(projected, apiVersion)
	if apiVersion == 1 && hash != "" {
		projected["hash"] = strings.ToUpper(hash)
	}
	return projected
}

// ProjectRaw applies the API-specific transaction projection to raw JSON
// without mutating the source document.
func ProjectRaw(raw json.RawMessage, hash string, apiVersion int) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var txJSON map[string]any
	if err := decoder.Decode(&txJSON); err != nil {
		return nil, err
	}
	return json.Marshal(ProjectJSON(txJSON, hash, apiVersion))
}

// InjectDeliverMax adds DeliverMax to Payment transaction JSON. API v1 keeps
// Amount, while API v2 and later replace it with DeliverMax.
func InjectDeliverMax(txJSON map[string]any, apiVersion int) {
	amount, hasAmount := txJSON["Amount"]
	if !hasAmount {
		return
	}
	txType, _ := txJSON["TransactionType"].(string)
	if txType != "Payment" {
		return
	}
	txJSON["DeliverMax"] = amount
	if apiVersion > 1 {
		delete(txJSON, "Amount")
	}
}
