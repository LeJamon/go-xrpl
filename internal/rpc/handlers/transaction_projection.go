package handlers

import (
	"bytes"
	"encoding/json"
	"maps"
	"strings"
)

func projectTransactionJSON(txJSON map[string]any, hash string, apiVersion int) map[string]any {
	projected := make(map[string]any, len(txJSON)+1)
	maps.Copy(projected, txJSON)
	injectDeliverMax(projected, apiVersion)
	if apiVersion == 1 && hash != "" {
		projected["hash"] = strings.ToUpper(hash)
	}
	return projected
}

// ProjectTransactionRaw applies the API-specific transaction projection used
// by subscription payloads without mutating the source JSON.
func ProjectTransactionRaw(raw json.RawMessage, hash string, apiVersion int) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var txJSON map[string]any
	if err := decoder.Decode(&txJSON); err != nil {
		return nil, err
	}
	return json.Marshal(projectTransactionJSON(txJSON, hash, apiVersion))
}
