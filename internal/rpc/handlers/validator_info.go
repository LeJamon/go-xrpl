package handlers

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// recentValidationsDefaultLimit is the cap applied when the caller
// does not pass a `limit`. 256 mirrors rippled's general admin-tier
// pagination default (e.g. account_tx for admin), bounding the
// archive scan without surprising small-deployment operators.
const recentValidationsDefaultLimit = 256

// recentValidationsMaxLimit is the hard ceiling regardless of what
// the caller asks for. 1000 prevents a misconfigured client from
// asking the SQL backend to materialise an unbounded result set.
const recentValidationsMaxLimit = 1000

// ValidatorInfoMethod handles the `validator_info` RPC method.
//
// Rippled reference: src/xrpld/rpc/handlers/ValidatorInfo.cpp:30-62.
//
// Strict rippled parity for the canonical fields (master_key,
// ephemeral_key, manifest, seq, domain) — the response is built the
// exact same way: validationPK is the configured signing key, the
// manifest cache resolves it to a master, and ephemeral/manifest
// metadata is only emitted when there IS a manifest mapping the two
// to different keys.
//
// goXRPL extension (issue #285): when a validation archive is wired,
// append a `recent_validations` array carrying the most-recent rows
// signed by this validator (DESC by ledger sequence). The shape uses
// rippled-flavoured encodings — hex-uppercase ledger hashes and unix
// epoch timestamps — so a forensic operator can replay them without
// further translation.
type ValidatorInfoMethod struct{ AdminHandler }

type validatorInfoResponse struct {
	MasterKey          string                  `json:"master_key,omitempty"`
	EphemeralKey       string                  `json:"ephemeral_key,omitempty"`
	Manifest           string                  `json:"manifest,omitempty"`
	Seq                uint32                  `json:"seq,omitempty"`
	Domain             string                  `json:"domain,omitempty"`
	RecentValidations  []recentValidationEntry `json:"recent_validations,omitempty"`
}

type recentValidationEntry struct {
	LedgerSeq  uint32 `json:"ledger_seq"`
	LedgerHash string `json:"ledger_hash"`
	SignTime   int64  `json:"sign_time,omitempty"`
	SeenTime   int64  `json:"seen_time,omitempty"`
	Flags      uint32 `json:"flags,omitempty"`
}

func (m *ValidatorInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	// Step 1: rippled checks `getValidationPublicKey()` first — empty
	// means the operator never configured a validation_seed (or token).
	// Mirror that exact gate so a non-validator deployment keeps
	// returning notValidator regardless of whether the archive
	// happens to be wired.
	if types.Services == nil || len(types.Services.ValidatorPublicKey) == 0 {
		return nil, types.NewRpcError(types.RpcNOT_VALIDATOR, "notValidator", "notValidator",
			"This server is not configured as a validator")
	}

	validationPK := types.Services.ValidatorPublicKey
	if len(validationPK) != 33 {
		// Defensive: the wiring code only sets this from a 33-byte
		// secp256k1 NodeID, so a wrong length is a programmer error
		// upstream rather than a user-input issue.
		return nil, types.RpcErrorInternal("validator public key has invalid length")
	}

	resp := validatorInfoResponse{}

	// Step 2: master-key resolution. Without a manifest cache (e.g.
	// standalone), GetMasterKey returns its input unchanged — which
	// is also rippled's behaviour when no manifest has arrived for
	// the key, so the no-cache branch and the cache-miss branch
	// converge on the same shape (master_key only, no ephemeral).
	var keyArr [33]byte
	copy(keyArr[:], validationPK)

	masterKey := keyArr
	if types.Services.Manifests != nil {
		masterKey = types.Services.Manifests.GetMasterKey(keyArr)
	}

	masterB58, err := addresscodec.EncodeNodePublicKey(masterKey[:])
	if err != nil {
		return nil, types.RpcErrorInternal("encode master key: " + err.Error())
	}
	resp.MasterKey = masterB58

	// Step 3: ephemeral / manifest / seq / domain only when the
	// resolved master differs from the configured key. Matches the
	// `if (mk == validationPK) return ret;` early-return in rippled.
	if masterKey != keyArr && types.Services.Manifests != nil {
		ephB58, err := addresscodec.EncodeNodePublicKey(keyArr[:])
		if err != nil {
			return nil, types.RpcErrorInternal("encode ephemeral key: " + err.Error())
		}
		resp.EphemeralKey = ephB58

		if manifestBytes, ok := types.Services.Manifests.GetManifest(masterKey); ok {
			resp.Manifest = base64.StdEncoding.EncodeToString(manifestBytes)
		}
		if seq, ok := types.Services.Manifests.GetSequence(masterKey); ok {
			resp.Seq = seq
		}
		if domain, ok := types.Services.Manifests.GetDomain(masterKey); ok && domain != "" {
			resp.Domain = domain
		}
	}

	// Step 4: archive extension. Drop in silently when nothing is
	// wired — the issue's third acceptance test pins this behaviour.
	if types.Services.ValidationArchive != nil {
		limit, rpcErr := parseRecentValidationsLimit(params)
		if rpcErr != nil {
			return nil, rpcErr
		}
		rows, err := types.Services.ValidationArchive.GetValidationsByValidator(validationPK, limit)
		if err != nil {
			return nil, types.RpcErrorInternal("validation archive lookup failed: " + err.Error())
		}
		if len(rows) > 0 {
			resp.RecentValidations = make([]recentValidationEntry, 0, len(rows))
			for _, r := range rows {
				resp.RecentValidations = append(resp.RecentValidations, recentValidationEntry{
					LedgerSeq:  r.LedgerSeq,
					LedgerHash: strings.ToUpper(hex.EncodeToString(r.LedgerHash[:])),
					SignTime:   r.SignTimeS,
					SeenTime:   r.SeenTimeS,
					Flags:      r.Flags,
				})
			}
		}
	}

	return resp, nil
}

// parseRecentValidationsLimit reads the optional `limit` request
// parameter and clamps it to [1, recentValidationsMaxLimit]. Missing
// or zero means "use the default". Negative values are rejected the
// same way rippled rejects negative pagination caps.
func parseRecentValidationsLimit(params json.RawMessage) (int, *types.RpcError) {
	if len(params) == 0 {
		return recentValidationsDefaultLimit, nil
	}
	var req struct {
		Limit *int `json:"limit"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return 0, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
	}
	if req.Limit == nil || *req.Limit == 0 {
		return recentValidationsDefaultLimit, nil
	}
	if *req.Limit < 0 {
		return 0, types.RpcErrorInvalidParams("limit must be non-negative")
	}
	if *req.Limit > recentValidationsMaxLimit {
		return recentValidationsMaxLimit, nil
	}
	return *req.Limit, nil
}
