package subscription

import (
	"bytes"
	"encoding/json"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

var validStreams = map[types.SubscriptionType]bool{
	types.SubLedger:               true,
	types.SubTransactions:         true,
	types.SubTransactionsProposed: true,
	"rt_transactions":             true,
	types.SubBookChanges:          true,
	types.SubValidations:          true,
	types.SubManifests:            true,
	types.SubPeerStatus:           true,
	types.SubServer:               true,
	types.SubConsensus:            true,
}

var validUnsubscribeStreams = func() map[types.SubscriptionType]bool {
	streams := make(map[types.SubscriptionType]bool, len(validStreams))
	for stream := range validStreams {
		streams[stream] = true
	}
	delete(streams, types.SubBookChanges)
	return streams
}()

var (
	xrpAccountID = [20]byte{}
	noAccountID  = [20]byte{19: 1}
)

func isValidXRPLAddress(address string) bool {
	return addresscodec.IsValidClassicAddress(address)
}

func jsonIsArray(raw json.RawMessage) bool {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '[':
			return true
		default:
			return false
		}
	}
	return false
}

func wireArrayElements(raw json.RawMessage, scope *RequestScope) (present, isArray bool, elements []json.RawMessage, rpcErr *types.RpcError) {
	if raw == nil {
		return false, false, nil, nil
	}
	if !jsonIsArray(raw) {
		return true, false, nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if _, err := decoder.Token(); err != nil {
		return true, true, elements, types.RpcErrorInvalidParams("Invalid parameters.")
	}
	for decoder.More() {
		var element json.RawMessage
		if err := decoder.Decode(&element); err != nil {
			return true, true, elements, types.RpcErrorInvalidParams("Invalid parameters.")
		}
		if rpcErr := scope.consumeRaw(1); rpcErr != nil {
			return true, true, elements, rpcErr
		}
		elements = append(elements, element)
	}
	if _, err := decoder.Token(); err != nil {
		return true, true, elements, types.RpcErrorInvalidParams("Invalid parameters.")
	}
	return true, true, elements, nil
}

func resolveStreams(wireDecoded bool, raw json.RawMessage, typed []types.SubscriptionType, scope *RequestScope) (bool, []types.SubscriptionType, *types.RpcError) {
	if !wireDecoded {
		return typed != nil, typed, nil
	}
	present, isArray, elements, rpcErr := wireArrayElements(raw, scope)
	if !present {
		return false, nil, nil
	}
	if !isArray {
		return true, nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}
	streams := make([]types.SubscriptionType, 0, len(elements))
	for _, element := range elements {
		var stream string
		if json.Unmarshal(element, &stream) != nil {
			return true, streams, types.RpcErrorMalformedStream()
		}
		streams = append(streams, types.SubscriptionType(stream))
	}
	return true, streams, rpcErr
}

func resolveAccounts(wireDecoded bool, raw json.RawMessage, typed []string, scope *RequestScope) (bool, []string, *types.RpcError) {
	if !wireDecoded {
		return typed != nil, typed, nil
	}
	present, isArray, elements, rpcErr := wireArrayElements(raw, scope)
	if rpcErr != nil {
		return present, nil, rpcErr
	}
	if !present {
		return false, nil, nil
	}
	if !isArray {
		return true, nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}
	accounts := make([]string, 0, len(elements))
	for _, element := range elements {
		var account string
		if json.Unmarshal(element, &account) != nil {
			return true, nil, nil
		}
		accounts = append(accounts, account)
	}
	return true, accounts, nil
}

func resolveBooks(wireDecoded bool, raw json.RawMessage, typed []types.BookRequest, scope *RequestScope) (bool, []types.BookRequest, *types.RpcError) {
	if !wireDecoded {
		return typed != nil, typed, nil
	}
	present, isArray, elements, rpcErr := wireArrayElements(raw, scope)
	if !present {
		return false, nil, nil
	}
	if !isArray {
		return true, nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}
	books := make([]types.BookRequest, 0, len(elements))
	for _, element := range elements {
		var book types.BookRequest
		if json.Unmarshal(element, &book) != nil {
			return true, books, types.RpcErrorInvalidParams("Invalid parameters.")
		}
		books = append(books, book)
	}
	return true, books, rpcErr
}

func bookSideObject(raw json.RawMessage) (map[string]json.RawMessage, *types.RpcError) {
	if raw == nil {
		return nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}
	var side map[string]json.RawMessage
	if json.Unmarshal(raw, &side) != nil {
		return nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}
	return side, nil
}
