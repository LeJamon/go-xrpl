package mpt

import (
	"encoding/hex"
	"encoding/json"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/ledger/entry"
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

// Reference: rippled MPTokenIssuanceSet.cpp preflight
func (m *MPTokenIssuanceSet) Validate() error {
	if err := m.BaseTx.Validate(); err != nil {
		return err
	}

	// DomainID and Holder cannot both be present
	// Reference: rippled MPTokenIssuanceSet.cpp:40-41
	if m.hasDomainID && m.Holder != "" {
		return tx.Errorf(tx.TemMALFORMED, "cannot specify both DomainID and Holder")
	}

	flags := m.GetFlags()

	if flags&^tfMPTokenIssuanceSetValidMask != 0 {
		return tx.Errorf(tx.TemINVALID_FLAG, "invalid flags for MPTokenIssuanceSet")
	}

	// Cannot set both tfMPTLock and tfMPTUnlock
	if (flags&MPTokenIssuanceSetFlagLock) != 0 && (flags&MPTokenIssuanceSetFlagUnlock) != 0 {
		return tx.Errorf(tx.TemINVALID_FLAG, "cannot set both tfMPTLock and tfMPTUnlock")
	}

	// MPTokenIssuanceID is required
	if m.MPTokenIssuanceID == "" {
		return tx.Errorf(tx.TemMALFORMED, "MPTokenIssuanceID is required")
	}

	if len(m.MPTokenIssuanceID) != 48 {
		return tx.Errorf(tx.TemMALFORMED, "MPTokenIssuanceID must be 48 hex characters")
	}

	if _, err := hex.DecodeString(m.MPTokenIssuanceID); err != nil {
		return tx.Errorf(tx.TemMALFORMED, "MPTokenIssuanceID must be valid hex")
	}

	// Holder cannot be the same as Account
	if m.Holder != "" && m.Holder == m.Account {
		return tx.Errorf(tx.TemMALFORMED, "Holder cannot be the same as Account")
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
func (m *MPTokenIssuanceSet) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("mptoken issuance set apply",
		"account", m.Account,
		"issuanceID", m.MPTokenIssuanceID,
		"flags", m.GetFlags(),
	)

	rules := ctx.Rules()
	txFlags := m.GetFlags()

	// Rules-dependent preflight: with featureSingleAssetVault,
	// the transaction must actually change something (flags or domain).
	// Reference: rippled MPTokenIssuanceSet.cpp:60-65
	if rules.Enabled(amendment.FeatureSingleAssetVault) {
		if txFlags == 0 && !m.hasDomainID {
			return tx.TemMALFORMED
		}
	}

	// Parse MPTokenIssuanceID
	var mptID [24]byte
	issuanceIDBytes, err := hex.DecodeString(m.MPTokenIssuanceID)
	if err != nil || len(issuanceIDBytes) != 24 {
		return tx.TemINVALID
	}
	copy(mptID[:], issuanceIDBytes)

	// Preclaim: issuance must exist
	issuanceKey := keylet.MPTIssuance(mptID)
	issuanceRaw, err := ctx.View.Read(issuanceKey)
	if err != nil || issuanceRaw == nil {
		ctx.Log.Warn("mptoken issuance set: issuance not found",
			"issuanceID", m.MPTokenIssuanceID,
		)
		return tx.TecOBJECT_NOT_FOUND
	}

	issuance, err := state.ParseMPTokenIssuance(issuanceRaw)
	if err != nil {
		ctx.Log.Error("mptoken issuance set: failed to parse issuance", "error", err)
		return tx.TefINTERNAL
	}

	// CanLock check is conditional on featureSingleAssetVault.
	// Without the amendment, any Set on an issuance without lsfMPTCanLock fails.
	// With the amendment, only lock/unlock operations require lsfMPTCanLock.
	// Reference: rippled MPTokenIssuanceSet.cpp:116-123
	if issuance.Flags&entry.LsfMPTCanLock == 0 {
		if !rules.Enabled(amendment.FeatureSingleAssetVault) {
			ctx.Log.Warn("mptoken issuance set: issuance does not have CanLock capability")
			return tx.TecNO_PERMISSION
		} else if txFlags&MPTokenIssuanceSetFlagLock != 0 || txFlags&MPTokenIssuanceSetFlagUnlock != 0 {
			ctx.Log.Warn("mptoken issuance set: issuance does not have CanLock capability")
			return tx.TecNO_PERMISSION
		}
	}

	// Caller must be the issuer
	if issuance.Issuer != ctx.AccountID {
		ctx.Log.Warn("mptoken issuance set: caller is not issuer")
		return tx.TecNO_PERMISSION
	}

	if m.Holder != "" {
		// Targeting a specific holder's MPToken
		return m.setHolderToken(ctx, issuanceKey, issuance, txFlags)
	}

	// DomainID preclaim checks (only when targeting the issuance, not a holder)
	// Reference: rippled MPTokenIssuanceSet.cpp:141-153
	if m.hasDomainID {
		if issuance.Flags&entry.LsfMPTRequireAuth == 0 {
			return tx.TecNO_PERMISSION
		}
		if m.DomainID != nil && *m.DomainID != zeroHash256 {
			// Non-zero domain: verify it exists
			domainIDBytes, err := hex.DecodeString(*m.DomainID)
			if err != nil || len(domainIDBytes) != 32 {
				return tx.TefINTERNAL
			}
			var domainKey [32]byte
			copy(domainKey[:], domainIDBytes)
			domainKL := keylet.PermissionedDomainByID(domainKey)
			exists, _ := ctx.View.Exists(domainKL)
			if !exists {
				return tx.TecOBJECT_NOT_FOUND
			}
		}
	}

	// Targeting the issuance itself
	return m.setIssuance(ctx, issuanceKey, issuance, txFlags)
}

// zeroHash256 is the 64-char hex string of a 32-byte zero hash.
const zeroHash256 = "0000000000000000000000000000000000000000000000000000000000000000"

// setHolderToken modifies a specific holder's MPToken (lock/unlock).
func (m *MPTokenIssuanceSet) setHolderToken(ctx *tx.ApplyContext, issuanceKey keylet.Keylet, issuance *state.MPTokenIssuanceData, txFlags uint32) tx.Result {
	holderID, err := state.DecodeAccountID(m.Holder)
	if err != nil {
		return tx.TemINVALID
	}

	// Holder account must exist
	// Reference: rippled MPTokenIssuanceSet.cpp:132 — ctx.view.exists(keylet::account(...))
	holderAcctKey := keylet.Account(holderID)
	holderExists, err := ctx.View.Exists(holderAcctKey)
	if err != nil || !holderExists {
		ctx.Log.Warn("mptoken issuance set: holder account does not exist",
			"holder", m.Holder,
		)
		return tx.TecNO_DST
	}

	// MPToken must exist
	tokenKey := keylet.MPToken(issuanceKey.Key, holderID)
	tokenRaw, err := ctx.View.Read(tokenKey)
	if err != nil || tokenRaw == nil {
		ctx.Log.Warn("mptoken issuance set: holder token not found",
			"holder", m.Holder,
		)
		return tx.TecOBJECT_NOT_FOUND
	}

	token, err := state.ParseMPToken(tokenRaw)
	if err != nil {
		ctx.Log.Error("mptoken issuance set: failed to parse holder token", "error", err)
		return tx.TefINTERNAL
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
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(tokenKey, updatedData); err != nil {
		ctx.Log.Error("mptoken issuance set: failed to update holder token", "error", err)
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// setIssuance modifies the issuance itself (lock/unlock and DomainID).
// Reference: rippled MPTokenIssuanceSet.cpp doApply()
func (m *MPTokenIssuanceSet) setIssuance(ctx *tx.ApplyContext, issuanceKey keylet.Keylet, issuance *state.MPTokenIssuanceData, txFlags uint32) tx.Result {
	// Toggle lock/unlock on the issuance
	if txFlags&MPTokenIssuanceSetFlagLock != 0 {
		issuance.Flags |= entry.LsfMPTLocked
	} else if txFlags&MPTokenIssuanceSetFlagUnlock != 0 {
		issuance.Flags &= ^entry.LsfMPTLocked
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
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(issuanceKey, updatedData); err != nil {
		ctx.Log.Error("mptoken issuance set: failed to update issuance", "error", err)
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}
