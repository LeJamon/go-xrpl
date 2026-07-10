package mpt

import (
	"encoding/hex"
	"encoding/json"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// MPTokenIssuanceSet modifies a multi-purpose token issuance.
type MPTokenIssuanceSet struct {
	tx.BaseTx

	// MPTokenIssuanceID is the ID of the issuance (required)
	MPTokenIssuanceID string `json:"MPTokenIssuanceID" xrpl:"MPTokenIssuanceID"`

	// Holder is the holder account (optional)
	// When set, the issuer is modifying a specific holder's MPToken
	Holder string `json:"Holder,omitempty" xrpl:"Holder,omitempty"`

	// DomainID is the permissioned domain for this issuance (optional).
	// When set, the issuance is restricted to the specified domain.
	// Requires featurePermissionedDomains AND featureSingleAssetVault.
	// Reference: rippled MPTokenIssuanceSet.cpp sfDomainID
	DomainID *string `json:"DomainID,omitempty" xrpl:"DomainID,omitempty"`

	// The three mutation fields below require featureDynamicMPT. A nil pointer
	// means the field is absent; a present field mutates the issuance in place.

	// MPTokenMetadata replaces (or, when empty, removes) the metadata.
	MPTokenMetadata *string `json:"MPTokenMetadata,omitempty" xrpl:"MPTokenMetadata,omitempty"`

	// TransferFee replaces (or, when zero, removes) the transfer fee.
	TransferFee *uint16 `json:"TransferFee,omitempty" xrpl:"TransferFee,omitempty"`

	// MutableFlags sets/clears the issuance's mutable capability flags
	// (tmfMPTSet*/tmfMPTClear* pairs).
	MutableFlags *uint32 `json:"MutableFlags,omitempty" xrpl:"MutableFlags,omitempty"`

	// hasDomainID tracks whether the DomainID field was present in the parsed JSON.
	// This is needed because DomainID can be the zero hash (clearing the domain).
	hasDomainID bool
}

// UnmarshalJSON handles DomainID field presence tracking.
func (m *MPTokenIssuanceSet) UnmarshalJSON(data []byte) error {
	type Alias MPTokenIssuanceSet
	aux := &struct {
		DomainID *string `json:"DomainID,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(m),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	if aux.DomainID != nil {
		m.DomainID = aux.DomainID
		m.hasDomainID = true
	}
	return nil
}

func NewMPTokenIssuanceSet(account, issuanceID string) *MPTokenIssuanceSet {
	return &MPTokenIssuanceSet{
		BaseTx:            *tx.NewBaseTx(tx.TypeMPTokenIssuanceSet, account),
		MPTokenIssuanceID: issuanceID,
	}
}

func (m *MPTokenIssuanceSet) TxType() tx.Type {
	return tx.TypeMPTokenIssuanceSet
}

// Validate holds MPTokenIssuanceSet's rules-independent preflight: the flags mask
// and the MPTokenIssuanceID structural checks. The rest of rippled's preflight
// body — the DomainID/Holder and lock/unlock shape checks, the no-op check and
// the DynamicMPT mutation checks — is amendment-dependent and lives in
// PreflightRules, so the whole body keeps rippled's order (notably the isMutate
// temDISABLED gate leading it, ahead of the DomainID/Holder temMALFORMED check).
// GetFlagsMask adopts the engine FlagsMasker seam with the MPTokenIssuanceSet
// invalid-flags mask (rippled MPTokenIssuanceSet::getFlagsMask =
// tfMPTokenIssuanceSetMask), checked at preflight0. The mask covers only sfFlags;
// the tfMPTLock/tfMPTUnlock exclusivity and sfMutableFlags checks stay in Validate.
func (m *MPTokenIssuanceSet) GetFlagsMask(rules *amendment.Rules) uint32 {
	return ^tfMPTokenIssuanceSetValidMask
}

// Reference: rippled MPTokenIssuanceSet.cpp getFlagsMask + preflight().
func (m *MPTokenIssuanceSet) Validate() error {
	if err := m.BaseTx.Validate(); err != nil {
		return err
	}

	// MPTokenIssuanceID is a required UINT192 (rippled enforces its presence and
	// 24-byte width at deserialization, before preflight).
	if m.MPTokenIssuanceID == "" {
		return ter.Errorf(ter.TemMALFORMED, "MPTokenIssuanceID is required")
	}
	if len(m.MPTokenIssuanceID) != 48 {
		return ter.Errorf(ter.TemMALFORMED, "MPTokenIssuanceID must be 48 hex characters")
	}
	if _, err := hex.DecodeString(m.MPTokenIssuanceID); err != nil {
		return ter.Errorf(ter.TemMALFORMED, "MPTokenIssuanceID must be valid hex")
	}

	return nil
}

// isMutate reports whether the transaction carries any DynamicMPT mutation
// field. Reference: rippled MPTokenIssuanceSet.cpp preflight (isMutate).
func (m *MPTokenIssuanceSet) isMutate() bool {
	return m.MutableFlags != nil || m.MPTokenMetadata != nil || m.TransferFee != nil
}

// PreflightRules is the body of rippled's MPTokenIssuanceSet::preflight, in its
// exact order. It leads with the mutation-fields-require-DynamicMPT temDISABLED
// gate (ahead of the DomainID/Holder and lock/unlock shape checks), so a tx
// carrying a mutation field before the amendment activates is rejected temDISABLED
// even when it is also otherwise malformed. Keeping the whole body here (rather
// than splitting the rules-free shape checks into Validate) is what preserves
// that intra-preflight order.
// Reference: rippled MPTokenIssuanceSet.cpp preflight().
func (m *MPTokenIssuanceSet) PreflightRules(rules *amendment.Rules) error {
	isMutate := m.isMutate()
	dynamicMPT := rules.Enabled(amendment.FeatureDynamicMPT)

	// Mutation fields require DynamicMPT — first check of the preflight body.
	if isMutate && !dynamicMPT {
		return ter.Errorf(ter.TemDISABLED, "mutation fields require DynamicMPT")
	}

	// DomainID and Holder cannot both be present.
	if m.hasDomainID && m.Holder != "" {
		return ter.Errorf(ter.TemMALFORMED, "cannot specify both DomainID and Holder")
	}

	// Cannot set both tfMPTLock and tfMPTUnlock.
	if (m.GetFlags()&MPTokenIssuanceSetFlagLock) != 0 && (m.GetFlags()&MPTokenIssuanceSetFlagUnlock) != 0 {
		return ter.Errorf(ter.TemINVALID_FLAG, "cannot set both tfMPTLock and tfMPTUnlock")
	}

	// Holder cannot be the same as Account.
	if m.Holder != "" && m.Holder == m.Account {
		return ter.Errorf(ter.TemMALFORMED, "Holder cannot be the same as Account")
	}

	// Under SingleAssetVault or DynamicMPT the transaction must change something.
	if rules.Enabled(amendment.FeatureSingleAssetVault) || dynamicMPT {
		if m.GetFlags() == 0 && !m.hasDomainID && !isMutate {
			return ter.Errorf(ter.TemMALFORMED, "MPTokenIssuanceSet changes nothing")
		}
	}

	if !dynamicMPT {
		return nil
	}

	if isMutate && m.Holder != "" {
		return ter.Errorf(ter.TemMALFORMED, "Holder not allowed when mutating issuance")
	}
	if isMutate && m.GetFlags()&tx.TfUniversalMask != 0 {
		return ter.Errorf(ter.TemMALFORMED, "flags not allowed when mutating issuance")
	}
	if m.TransferFee != nil && *m.TransferFee > entry.MaxTransferFee {
		return ter.Errorf(ter.TemBAD_TRANSFER_FEE, "TransferFee cannot exceed 50000")
	}
	if m.MPTokenMetadata != nil {
		metadataBytes, err := hex.DecodeString(*m.MPTokenMetadata)
		if err != nil {
			return ter.Errorf(ter.TemMALFORMED, "MPTokenMetadata must be valid hex")
		}
		if len(metadataBytes) > entry.MaxMPTokenMetadataLength {
			return ter.Errorf(ter.TemMALFORMED, "MPTokenMetadata exceeds maximum length")
		}
	}
	if m.MutableFlags != nil {
		mf := *m.MutableFlags
		if mf == 0 || mf&tmfMPTokenIssuanceSetMutableMask != 0 {
			return ter.Errorf(ter.TemINVALID_FLAG, "invalid MutableFlags for MPTokenIssuanceSet")
		}
		for _, f := range mptMutabilityFlags {
			if mf&f.set != 0 && mf&f.clear != 0 {
				return ter.Errorf(ter.TemINVALID_FLAG, "cannot set and clear the same mutable flag")
			}
		}
		// Setting a non-zero TransferFee while clearing MPTCanTransfer is
		// contradictory.
		if m.TransferFee != nil && *m.TransferFee > 0 && mf&TmfMPTClearCanTransfer != 0 {
			return ter.Errorf(ter.TemMALFORMED, "cannot set TransferFee and clear MPTCanTransfer together")
		}
	}

	return nil
}

func (m *MPTokenIssuanceSet) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(m)
}

func (m *MPTokenIssuanceSet) RequiredAmendments() [][32]byte {
	amendments := [][32]byte{amendment.FeatureMPTokensV1}
	// DomainID requires both PermissionedDomains and SingleAssetVault
	// Reference: rippled MPTokenIssuanceSet.cpp:35-38
	if m.hasDomainID {
		amendments = append(amendments, amendment.FeaturePermissionedDomains, amendment.FeatureSingleAssetVault)
	}
	return amendments
}

// Reference: rippled MPTokenIssuanceSet.cpp preclaim() + doApply()
func (m *MPTokenIssuanceSet) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("mptoken issuance set apply",
		"account", m.Account,
		"issuanceID", m.MPTokenIssuanceID,
		"flags", m.GetFlags(),
	)

	rules := ctx.Rules()
	txFlags := m.GetFlags()

	// Parse MPTokenIssuanceID
	var mptID [24]byte
	issuanceIDBytes, err := hex.DecodeString(m.MPTokenIssuanceID)
	if err != nil || len(issuanceIDBytes) != 24 {
		return ter.TemINVALID
	}
	copy(mptID[:], issuanceIDBytes)

	// Preclaim: issuance must exist
	issuanceKey := keylet.MPTIssuance(mptID)
	issuanceRaw, err := ctx.View.Read(issuanceKey)
	if err != nil || issuanceRaw == nil {
		ctx.Log.Warn("mptoken issuance set: issuance not found",
			"issuanceID", m.MPTokenIssuanceID,
		)
		return ter.TecOBJECT_NOT_FOUND
	}

	issuance, err := state.ParseMPTokenIssuance(issuanceRaw)
	if err != nil {
		ctx.Log.Error("mptoken issuance set: failed to parse issuance", "error", err)
		return ter.TefINTERNAL
	}

	// CanLock check. Before SingleAssetVault / DynamicMPT, any Set on an
	// issuance without lsfMPTCanLock fails. With either amendment, only
	// lock/unlock operations require lsfMPTCanLock.
	// Reference: rippled MPTokenIssuanceSet.cpp:189-197
	if issuance.Flags&entry.LsfMPTCanLock == 0 {
		if !rules.Enabled(amendment.FeatureSingleAssetVault) && !rules.Enabled(amendment.FeatureDynamicMPT) {
			ctx.Log.Warn("mptoken issuance set: issuance does not have CanLock capability")
			return ter.TecNO_PERMISSION
		} else if txFlags&MPTokenIssuanceSetFlagLock != 0 || txFlags&MPTokenIssuanceSetFlagUnlock != 0 {
			ctx.Log.Warn("mptoken issuance set: issuance does not have CanLock capability")
			return ter.TecNO_PERMISSION
		}
	}

	// Caller must be the issuer
	if issuance.Issuer != ctx.AccountID {
		ctx.Log.Warn("mptoken issuance set: caller is not issuer")
		return ter.TecNO_PERMISSION
	}

	if m.Holder != "" {
		// Targeting a specific holder's MPToken
		return m.setHolderToken(ctx, issuanceKey, issuance, txFlags)
	}

	// DomainID preclaim checks (only when targeting the issuance, not a holder)
	// Reference: rippled MPTokenIssuanceSet.cpp:141-153
	if m.hasDomainID {
		if issuance.Flags&entry.LsfMPTRequireAuth == 0 {
			return ter.TecNO_PERMISSION
		}
		if m.DomainID != nil && *m.DomainID != zeroHash256 {
			// Non-zero domain: verify it exists
			domainIDBytes, err := hex.DecodeString(*m.DomainID)
			if err != nil || len(domainIDBytes) != 32 {
				return ter.TefINTERNAL
			}
			var domainKey [32]byte
			copy(domainKey[:], domainIDBytes)
			domainKL := keylet.PermissionedDomainByID(domainKey)
			exists, _ := ctx.View.Exists(domainKL)
			if !exists {
				return ter.TecOBJECT_NOT_FOUND
			}
		}
	}

	// Targeting the issuance itself
	return m.setIssuance(ctx, issuanceKey, issuance, txFlags)
}

// zeroHash256 is the 64-char hex string of a 32-byte zero hash.
const zeroHash256 = "0000000000000000000000000000000000000000000000000000000000000000"

// setHolderToken modifies a specific holder's MPToken (lock/unlock).
func (m *MPTokenIssuanceSet) setHolderToken(ctx *tx.ApplyContext, issuanceKey keylet.Keylet, issuance *state.MPTokenIssuanceData, txFlags uint32) ter.Result {
	holderID, err := state.DecodeAccountID(m.Holder)
	if err != nil {
		return ter.TemINVALID
	}

	// Holder account must exist
	// Reference: rippled MPTokenIssuanceSet.cpp:132 — ctx.view.exists(keylet::account(...))
	holderAcctKey := keylet.Account(holderID)
	holderExists, err := ctx.View.Exists(holderAcctKey)
	if err != nil || !holderExists {
		ctx.Log.Warn("mptoken issuance set: holder account does not exist",
			"holder", m.Holder,
		)
		return ter.TecNO_DST
	}

	// MPToken must exist
	tokenKey := keylet.MPToken(issuanceKey.Key, holderID)
	tokenRaw, err := ctx.View.Read(tokenKey)
	if err != nil || tokenRaw == nil {
		ctx.Log.Warn("mptoken issuance set: holder token not found",
			"holder", m.Holder,
		)
		return ter.TecOBJECT_NOT_FOUND
	}

	token, err := state.ParseMPToken(tokenRaw)
	if err != nil {
		ctx.Log.Error("mptoken issuance set: failed to parse holder token", "error", err)
		return ter.TefINTERNAL
	}

	// Toggle lock/unlock on the token
	if txFlags&MPTokenIssuanceSetFlagLock != 0 {
		token.Flags |= entry.LsfMPTLocked
	} else if txFlags&MPTokenIssuanceSetFlagUnlock != 0 {
		token.Flags &= ^entry.LsfMPTLocked
	}

	// Serialize and update
	updatedData, err := state.SerializeMPToken(token)
	if err != nil {
		ctx.Log.Error("mptoken issuance set: failed to serialize holder token", "error", err)
		return ter.TefINTERNAL
	}
	if err := ctx.View.Update(tokenKey, updatedData); err != nil {
		ctx.Log.Error("mptoken issuance set: failed to update holder token", "error", err)
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

// setIssuance modifies the issuance itself (lock/unlock, mutation fields, and
// DomainID). Reference: rippled MPTokenIssuanceSet.cpp preclaim() + doApply()
func (m *MPTokenIssuanceSet) setIssuance(ctx *tx.ApplyContext, issuanceKey keylet.Keylet, issuance *state.MPTokenIssuanceData, txFlags uint32) ter.Result {
	// Preclaim: every mutation must be permitted by a CanMutate bit set at
	// issuance. Reference: rippled MPTokenIssuanceSet.cpp:229-265.
	if result := m.checkMutablePermissions(issuance); result != ter.TesSUCCESS {
		return result
	}

	// Toggle lock/unlock on the issuance
	if txFlags&MPTokenIssuanceSetFlagLock != 0 {
		issuance.Flags |= entry.LsfMPTLocked
	} else if txFlags&MPTokenIssuanceSetFlagUnlock != 0 {
		issuance.Flags &= ^entry.LsfMPTLocked
	}

	// Apply mutable-flag set/clear pairs. Each toggles the matching lsf* flag
	// (numerically equal to canMutate). Clearing MPTCanTransfer also drops the
	// TransferFee field. Reference: rippled MPTokenIssuanceSet.cpp:295-311.
	if m.MutableFlags != nil {
		mf := *m.MutableFlags
		for _, f := range mptMutabilityFlags {
			if mf&f.set != 0 {
				issuance.Flags |= f.canMutate
			} else if mf&f.clear != 0 {
				issuance.Flags &= ^f.canMutate
			}
		}
		if mf&TmfMPTClearCanTransfer != 0 {
			issuance.TransferFee = 0
		}
	}

	// TransferFee: zero removes the field (the serializer omits it), a non-zero
	// value replaces it. Reference: rippled MPTokenIssuanceSet.cpp:316-326.
	if m.TransferFee != nil {
		issuance.TransferFee = *m.TransferFee
	}

	// Metadata: empty removes the field, non-empty replaces it.
	// Reference: rippled MPTokenIssuanceSet.cpp:328-334.
	if m.MPTokenMetadata != nil {
		issuance.MPTokenMetadata = *m.MPTokenMetadata
	}

	// Handle DomainID update
	// Reference: rippled MPTokenIssuanceSet.cpp:186-202
	if m.hasDomainID && m.DomainID != nil {
		if *m.DomainID != zeroHash256 {
			issuance.DomainID = m.DomainID
		} else {
			// Clear the DomainID (zero hash means remove)
			issuance.DomainID = nil
		}
	}

	// Serialize and update
	updatedData, err := state.SerializeMPTokenIssuance(issuance)
	if err != nil {
		ctx.Log.Error("mptoken issuance set: failed to serialize issuance", "error", err)
		return ter.TefINTERNAL
	}
	if err := ctx.View.Update(issuanceKey, updatedData); err != nil {
		ctx.Log.Error("mptoken issuance set: failed to update issuance", "error", err)
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

// checkMutablePermissions rejects any mutation the issuance did not opt into via
// its sfMutableFlags (soeDEFAULT, so 0 when absent).
// Reference: rippled MPTokenIssuanceSet.cpp:229-265.
func (m *MPTokenIssuanceSet) checkMutablePermissions(issuance *state.MPTokenIssuanceData) ter.Result {
	if m.MutableFlags != nil {
		mf := *m.MutableFlags
		for _, f := range mptMutabilityFlags {
			if issuance.MutableFlags&f.canMutate == 0 && mf&(f.set|f.clear) != 0 {
				return ter.TecNO_PERMISSION
			}
		}
		// A DomainID requires RequireAuth to stay active, so clearing RequireAuth
		// while the issuance already carries a DomainID is disallowed.
		if mf&TmfMPTClearRequireAuth != 0 && issuance.DomainID != nil {
			return ter.TecNO_PERMISSION
		}
	}

	if m.MPTokenMetadata != nil && issuance.MutableFlags&entry.LsmfMPTCanMutateMetadata == 0 {
		return ter.TecNO_PERMISSION
	}

	if m.TransferFee != nil {
		// A non-zero fee only makes sense once MPTCanTransfer is already set;
		// enabling it in the same transaction does not satisfy this.
		if *m.TransferFee > 0 && issuance.Flags&entry.LsfMPTCanTransfer == 0 {
			return ter.TecNO_PERMISSION
		}
		if issuance.MutableFlags&entry.LsmfMPTCanMutateTransferFee == 0 {
			return ter.TecNO_PERMISSION
		}
	}

	return ter.TesSUCCESS
}
