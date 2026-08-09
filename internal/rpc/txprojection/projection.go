// Package txprojection contains the API-version-specific transaction JSON
// shaping shared by RPC handlers and WebSocket publishers.
package txprojection

import (
	"bytes"
	"encoding/json"
	"maps"
	"strings"
)

// Path identifies the response contract that owns transaction projection.
type Path uint8

const (
	PathSigned Path = iota
	PathCanonical
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

// ProjectJSONForPath returns an immutable transaction copy using the endpoint
// and API-version-specific response contract. Values reachable through the
// source map are never changed by the projection.
func ProjectJSONForPath(txJSON map[string]any, hash string, apiVersion int, path Path) map[string]any {
	projected := make(map[string]any, len(txJSON)+1)
	maps.Copy(projected, txJSON)

	// Hash is a response field, not a serialized transaction field. Remove any
	// source copy before applying the path-specific placement below.
	delete(projected, "hash")
	if path != PathCanonical {
		InjectDeliverMax(projected, apiVersion)
	}

	if hash == "" {
		return projected
	}
	hash = strings.ToUpper(hash)
	if path == PathCanonical || apiVersion == 1 {
		projected["hash"] = hash
	}
	return projected
}

// FormatResult builds a transaction response without mutating the source map.
func FormatResult(txJSON map[string]any, txBlob, hash string, apiVersion int, path Path) map[string]any {
	response := map[string]any{
		"tx_blob": txBlob,
		"tx_json": ProjectJSONForPath(txJSON, hash, apiVersion, path),
	}
	if path != PathCanonical && apiVersion > 1 && hash != "" {
		response["hash"] = strings.ToUpper(hash)
	}
	return response
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
