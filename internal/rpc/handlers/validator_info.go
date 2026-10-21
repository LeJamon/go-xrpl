package handlers

import (
	"encoding/base64"
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
)

// ValidatorInfoMethod handles the `validator_info` RPC method.
//
// Rippled reference: src/xrpld/rpc/handlers/ValidatorInfo.cpp:30-62.
// The notValidator gate maps to rippled's not_validator_error() —
// make_param_error("not a validator") — i.e. rpcINVALID_PARAMS with
// the literal "not a validator" message. SDK tooling (xrpl.js, xrpl-py)
// pattern-matches on that exact wire shape.
type ValidatorInfoMethod struct{ adminHandler }

func (m *ValidatorInfoMethod) Handle(ctx *types.RpcContext, _ json.RawMessage) (any, *rpcerrors.RpcError) {
	if ctx.Services == nil || len(ctx.Services.ValidatorPublicKey()) == 0 {
		return nil, rpcerrors.RpcErrorInvalidParams("not a validator")
	}

	validationPK := ctx.Services.ValidatorPublicKey()
	if len(validationPK) != 33 {
		return nil, rpcInternalInvariantError("validator_info: validator public key has invalid length")
	}

	var keyArr [33]byte
	copy(keyArr[:], validationPK)

	masterKey := keyArr
	if ctx.Services.Manifests() != nil {
		masterKey = ctx.Services.Manifests().GetMasterKey(keyArr)
	}

	masterB58, err := addresscodec.EncodeNodePublicKey(masterKey[:])
	if err != nil {
		return nil, rpcInternalError("validator_info: master key encoding failed", err)
	}
	resp := map[string]any{"master_key": masterB58}

	// rippled: `if (mk == validationPK) return ret;` — only emit the
	// ephemeral / manifest / seq / domain block when a manifest cache
	// resolved the configured signing key to a different master.
	// (masterKey != keyArr already implies Manifests != nil, since the
	// nil branch above leaves masterKey == keyArr.)
	if masterKey != keyArr {
		ephB58, err := addresscodec.EncodeNodePublicKey(keyArr[:])
		if err != nil {
			return nil, rpcInternalError("validator_info: ephemeral key encoding failed", err)
		}
		resp["ephemeral_key"] = ephB58

		if manifestBytes, ok := ctx.Services.Manifests().GetManifest(masterKey); ok {
			resp["manifest"] = base64.StdEncoding.EncodeToString(manifestBytes)
		}
		if seq, ok := ctx.Services.Manifests().GetSequence(masterKey); ok {
			resp["seq"] = seq
		}
		if domain, ok := ctx.Services.Manifests().GetDomain(masterKey); ok {
			resp["domain"] = domain
		}
	}

	return resp, nil
}
