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
	"github.com/LeJamon/go-xrpl/protocol"
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

	// ImmutableFlags permanently prevents later changes to selected capabilities
	// and fields.
	ImmutableFlags *uint32 `json:"ImmutableFlags,omitempty" xrpl:"ImmutableFlags,omitempty"`

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
// the tfMPTLock/tfMPTUnlock exclusivity and sfImmutableFlags checks stay in Validate.
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
	return m.GetFlags()&tfMPTokenIssuanceSetEnableFlagMask != 0 ||
		m.ImmutableFlags != nil || m.MPTokenMetadata != nil || m.TransferFee != nil
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
	enableFlags := m.GetFlags() & tfMPTokenIssuanceSetEnableFlagMask

	// Mutation fields require DynamicMPT — first check of the preflight body.
	if isMutate && !dynamicMPT {
		return ter.Errorf(ter.TemDISABLED, "mutation fields require DynamicMPT")
	}
	if (enableFlags&MPTokenIssuanceSetFlagSetCanHoldConfidentialBalance != 0 ||
		(m.ImmutableFlags != nil && *m.ImmutableFlags&TifMPTCanHoldConfidentialBalance != 0)) &&
		!rules.Enabled(amendment.FeatureConfidentialTransfer) {
		return ter.Errorf(ter.TemDISABLED, "confidential balance capability requires ConfidentialTransfer")
	}

	// DomainID and Holder cannot both be present.
	if m.hasDomainID && m.Holder != "" {
		return ter.Errorf(ter.TemMALFORMED, "cannot specify both DomainID and Holder")
	}
	if enableFlags&MPTokenIssuanceSetFlagSetCanHoldConfidentialBalance != 0 && m.Holder != "" {
		return ter.Errorf(ter.TemMALFORMED, "confidential balance capability cannot target a holder")
	}

	// Cannot set both tfMPTLock and tfMPTUnlock.
	if (m.GetFlags()&MPTokenIssuanceSetFlagLock) != 0 && (m.GetFlags()&MPTokenIssuanceSetFlagUnlock) != 0 {
		return ter.Errorf(ter.TemINVALID_FLAG, "cannot set both tfMPTLock and tfMPTUnlock")
	}

	// Holder cannot be the same as Account.
	if m.Holder != "" && m.Holder == m.Account {
		return ter.Errorf(ter.TemMALFORMED, "Holder cannot be the same as Account")
	}

	// Under the amendments that extend this transaction, it must change something.
	if rules.Enabled(amendment.FeatureSingleAssetVault) || dynamicMPT ||
		rules.Enabled(amendment.FeatureConfidentialTransfer) {
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
	if isMutate && m.GetFlags()&(MPTokenIssuanceSetFlagLock|MPTokenIssuanceSetFlagUnlock) != 0 {
		return ter.Errorf(ter.TemMALFORMED, "lock or unlock cannot be combined with an issuance mutation")
	}
	if m.TransferFee != nil && *m.TransferFee > protocol.MaxMPTokenTransferFee {
		return ter.Errorf(ter.TemBAD_TRANSFER_FEE, "TransferFee cannot exceed 50000")
	}
	if m.TransferFee != nil && *m.TransferFee > 0 && enableFlags&MPTokenIssuanceSetFlagSetCanHoldConfidentialBalance != 0 {
		return ter.Errorf(ter.TemBAD_TRANSFER_FEE, "TransferFee is incompatible with confidential balances")
	}
	if m.MPTokenMetadata != nil {
		metadataBytes, err := hex.DecodeString(*m.MPTokenMetadata)
		if err != nil {
			return ter.Errorf(ter.TemMALFORMED, "MPTokenMetadata must be valid hex")
		}
		if len(metadataBytes) > protocol.MaxMPTokenMetadataLength {
			return ter.Errorf(ter.TemMALFORMED, "MPTokenMetadata exceeds maximum length")
		}
	}
	if m.ImmutableFlags != nil {
		immutableFlags := *m.ImmutableFlags
		if immutableFlags == 0 || immutableFlags&tifMPTokenIssuanceImmutableMask != 0 {
			return ter.Errorf(ter.TemINVALID_FLAG, "invalid ImmutableFlags for MPTokenIssuanceSet")
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

// Preclaim runs MPTokenIssuanceSet's ledger-aware checks in rippled
// MPTokenIssuanceSet::preclaim order: issuance exists (tecOBJECT_NOT_FOUND); the
// CanLock gate (tecNO_PERMISSION); issuer match (tecNO_PERMISSION); for a Holder
// target the holder account (tecNO_DST) and its MPToken (tecOBJECT_NOT_FOUND)
// must exist; otherwise the DomainID gate (RequireAuth tecNO_PERMISSION + domain
// existence tecOBJECT_NOT_FOUND) and the CanMutate permission checks. The
// lock/unlock, flag, fee, metadata, and DomainID mutations stay in Apply.
func (m *MPTokenIssuanceSet) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	rules := view.Rules()
	txFlags := m.GetFlags()

	var mptID [24]byte
	b, err := hex.DecodeString(m.MPTokenIssuanceID)
	if err != nil || len(b) != 24 {
		return ter.TemINVALID
	}
	copy(mptID[:], b)
	issuanceKey := keylet.MPTIssuance(mptID)
	raw, rerr := view.Read(issuanceKey)
	if rerr != nil || raw == nil {
		return ter.TecOBJECT_NOT_FOUND
	}
	issuance, perr := state.ParseMPTokenIssuance(raw)
	if perr != nil {
		return ter.TefINTERNAL
	}

	if issuance.Flags&entry.LsfMPTCanLock == 0 {
		if rules == nil || (!rules.Enabled(amendment.FeatureSingleAssetVault) && !rules.Enabled(amendment.FeatureDynamicMPT)) {
			return ter.TecNO_PERMISSION
		}
		if txFlags&MPTokenIssuanceSetFlagLock != 0 || txFlags&MPTokenIssuanceSetFlagUnlock != 0 {
			return ter.TecNO_PERMISSION
		}
	}

	accountID, aerr := state.DecodeAccountID(m.Account)
	if aerr != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	if issuance.Issuer != accountID {
		return ter.TecNO_PERMISSION
	}

	if m.Holder != "" {
		holderID, herr := state.DecodeAccountID(m.Holder)
		if herr != nil {
			return ter.TemINVALID
		}
		if exists, _ := view.Exists(keylet.Account(holderID)); !exists {
			return ter.TecNO_DST
		}
		if exists, _ := view.Exists(keylet.MPToken(issuanceKey.Key, holderID)); !exists {
			return ter.TecOBJECT_NOT_FOUND
		}
		return ter.TesSUCCESS
	}

	if m.hasDomainID {
		if issuance.Flags&entry.LsfMPTRequireAuth == 0 {
			return ter.TecNO_PERMISSION
		}
		if m.DomainID != nil && *m.DomainID != zeroHash256 {
			db, derr := hex.DecodeString(*m.DomainID)
			if derr != nil || len(db) != 32 {
				return ter.TefINTERNAL
			}
			var dk [32]byte
			copy(dk[:], db)
			if exists, _ := view.Exists(keylet.PermissionedDomainByID(dk)); !exists {
				return ter.TecOBJECT_NOT_FOUND
			}
		}
	}

	return m.checkImmutablePermissions(issuance, txFlags)
}

// Reference: rippled MPTokenIssuanceSet.cpp doApply(); the ledger-aware gates
// live in Preclaim.
func (m *MPTokenIssuanceSet) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("mptoken issuance set apply",
		"account", m.Account,
		"issuanceID", m.MPTokenIssuanceID,
		"flags", m.GetFlags(),
	)

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

	if m.Holder != "" {
		// Targeting a specific holder's MPToken
		return m.setHolderToken(ctx, issuanceKey, issuance, txFlags)
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

	// Holder account and MPToken existence are gated in Preclaim. The token is
	// read here because the lock/unlock mutation needs it.
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
	// The CanMutate permission checks and DomainID existence gate live in
	// Preclaim (rippled MPTokenIssuanceSet::preclaim).

	// Toggle lock/unlock on the issuance
	if txFlags&MPTokenIssuanceSetFlagLock != 0 {
		issuance.Flags |= entry.LsfMPTLocked
	} else if txFlags&MPTokenIssuanceSetFlagUnlock != 0 {
		issuance.Flags &= ^entry.LsfMPTLocked
	}

	// Capability flags are one-way: DynamicMPT may enable them, never clear them.
	if enableFlags := txFlags & tfMPTokenIssuanceSetEnableFlagMask; enableFlags != 0 {
		for _, capability := range mptCapabilities {
			if enableFlags&capability.setFlag != 0 {
				issuance.Flags |= capability.ledgerFlag
			}
		}
	}

	if m.ImmutableFlags != nil {
		issuance.ImmutableFlags |= *m.ImmutableFlags
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

// checkImmutablePermissions rejects mutations blocked by sfImmutableFlags.
func (m *MPTokenIssuanceSet) checkImmutablePermissions(issuance *state.MPTokenIssuanceData, txFlags uint32) ter.Result {
	for _, capability := range mptCapabilities {
		if txFlags&capability.setFlag != 0 && issuance.ImmutableFlags&capability.immutableFlag != 0 {
			return ter.TecNO_PERMISSION
		}
	}

	if m.MPTokenMetadata != nil && issuance.ImmutableFlags&TifMPTMetadata != 0 {
		return ter.TecNO_PERMISSION
	}

	if m.TransferFee != nil {
		if *m.TransferFee > 0 && issuance.Flags&entry.LsfMPTCanTransfer == 0 &&
			txFlags&MPTokenIssuanceSetFlagSetCanTransfer == 0 {
			return ter.TecNO_PERMISSION
		}
		if *m.TransferFee > 0 && issuance.Flags&entry.LsfMPTCanHoldConfidentialBalance != 0 {
			return ter.TecNO_PERMISSION
		}
		if issuance.ImmutableFlags&TifMPTTransferFee != 0 {
			return ter.TecNO_PERMISSION
		}
	}

	if txFlags&MPTokenIssuanceSetFlagSetCanHoldConfidentialBalance != 0 && issuance.TransferFee > 0 {
		return ter.TecNO_PERMISSION
	}

	return ter.TesSUCCESS
}
