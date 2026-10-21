package handlers

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/crypto"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// ManifestMethod handles the manifest RPC method.
type ManifestMethod struct{ baseHandler }

func (m *ManifestMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	fields, rpcErr := rawJSONFields(params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	raw, present := fields["public_key"]
	if !present {
		return nil, rpcerrors.RpcErrorMissingField("public_key")
	}
	requested, rpcErr := manifestRequestedKey(raw)
	if rpcErr != nil {
		if rpcErr.Code == rpcerrors.RpcINTERNAL {
			ctx.LoadCost = uint32(resource.FeeExceptionRPC().Cost())
		}
		return nil, rpcErr
	}
	response := map[string]any{"requested": requested}
	rawKey, err := addresscodec.DecodeNodePublicKey(requested)
	if err != nil || crypto.PublicKeyType(rawKey) == crypto.KeyTypeUnknown {
		return nil, rpcerrors.RpcErrorInvalidParams("").WithExtra(response)
	}
	var master [33]byte
	copy(master[:], rawKey)
	ephemeral := master
	cache := ctx.Services.Manifests()
	if cache != nil {
		master = cache.GetMasterKey(master)
		var found bool
		ephemeral, found = cache.GetSigningKey(master)
		if !found {
			ephemeral = master
		}
	}

	masterB58, err := addresscodec.EncodeNodePublicKey(master[:])
	if err != nil {
		return nil, rpcInternalError("manifest: master key encoding failed", err)
	}
	ephemeralB58, err := addresscodec.EncodeNodePublicKey(ephemeral[:])
	if err != nil {
		return nil, rpcInternalError("manifest: ephemeral key encoding failed", err)
	}
	details := map[string]any{
		"master_key":    masterB58,
		"ephemeral_key": ephemeralB58,
	}
	if cache != nil {
		if serialized, ok := cache.GetManifest(master); ok {
			response["manifest"] = base64.StdEncoding.EncodeToString(serialized)
		}
		if seq, ok := cache.GetSequence(master); ok {
			details["seq"] = seq
		}
		if domain, ok := cache.GetDomain(master); ok {
			details["domain"] = domain
		}
	}
	response["details"] = details
	return response, nil
}

func manifestRequestedKey(raw json.RawMessage) (string, *rpcerrors.RpcError) {
	value, err := decodeRawJSONValue(raw)
	if err != nil {
		return "", rpcerrors.RpcErrorInvalidParams("")
	}
	switch value := value.(type) {
	case nil:
		return "", nil
	case string:
		value, _, _ = strings.Cut(value, "\x00")
		return value, nil
	case bool:
		return strconv.FormatBool(value), nil
	case json.Number:
		if strings.ContainsAny(value.String(), ".eE") {
			number, err := value.Float64()
			if err != nil {
				return "", rpcerrors.RpcErrorInvalidParams("")
			}
			return strconv.FormatFloat(number, 'f', 6, 64), nil
		}
		if strings.HasPrefix(value.String(), "-") {
			number, err := strconv.ParseInt(value.String(), 10, 32)
			if err == nil {
				return strconv.FormatInt(number, 10), nil
			}
		} else if number, err := strconv.ParseUint(value.String(), 10, 32); err == nil {
			return strconv.FormatUint(number, 10), nil
		}
		return "", rpcerrors.RpcErrorInvalidParams("")
	default:
		return "", rpcerrors.RpcErrorInternal()
	}
}

func (m *ManifestMethod) RequiredRole() types.Role {
	return types.RoleUser
}
