package credential

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

// mapView is a minimal in-memory tx.LedgerView for helper-level tests.
type mapView struct{ data map[[32]byte][]byte }

func newMapView() *mapView { return &mapView{data: make(map[[32]byte][]byte)} }

func (m *mapView) Read(k keylet.Keylet) ([]byte, error)       { return m.data[k.Key], nil }
func (m *mapView) Exists(k keylet.Keylet) (bool, error)       { _, ok := m.data[k.Key]; return ok, nil }
func (m *mapView) Insert(k keylet.Keylet, data []byte) error  { m.data[k.Key] = data; return nil }
func (m *mapView) Update(k keylet.Keylet, data []byte) error  { m.data[k.Key] = data; return nil }
func (m *mapView) Erase(k keylet.Keylet) error                { delete(m.data, k.Key); return nil }
func (m *mapView) AdjustDropsDestroyed(drops.XRPAmount) error { return nil }
func (m *mapView) ForEach(fn func(key [32]byte, data []byte) bool) error {
	for k, v := range m.data {
		if !fn(k, v) {
			break
		}
	}
	return nil
}
func (m *mapView) Succ([32]byte) ([32]byte, []byte, bool, error) { return [32]byte{}, nil, false, nil }
func (m *mapView) TxExists([32]byte) (bool, error)               { return false, nil }
func (m *mapView) Rules() *amendment.Rules                       { return nil }
func (m *mapView) LedgerSeq() uint32                             { return 0 }

// TestRemoveExpiredCredentials_DeletionFailure mirrors rippled's
// testRemoveExpiredCorruption (Credentials_test.cpp): an expired accepted
// credential whose issuer account has been erased makes deleteSLE fail with
// tecINTERNAL. Under fixCleanup3_1_3 that failure aborts verifyDepositPreauth
// (tecINTERNAL); before the amendment it is swallowed and the caller still
// returns tecEXPIRED. (PR #6715)
func TestRemoveExpiredCredentials_DeletionFailure(t *testing.T) {
	var subjectID, issuerID [20]byte
	subjectID[0], subjectID[19] = 0x01, 0x11
	issuerID[0], issuerID[19] = 0x02, 0x22

	subjectAddr, err := addresscodec.EncodeAccountIDToClassicAddress(subjectID[:])
	require.NoError(t, err)

	credType := []byte("abcde")
	credKey := keylet.Credential(subjectID, issuerID, credType)
	credIDHex := hex.EncodeToString(credKey.Key[:])

	const expiration = uint32(100)
	const closeTime = uint32(200) // strictly after expiration -> expired

	buildCtx := func(rules *amendment.Rules) *tx.ApplyContext {
		view := newMapView()

		// Accepted, expired credential owned across the subject and issuer dirs.
		exp := expiration
		credBlob, serr := serializeCredentialEntry(&CredentialEntry{
			Subject:        subjectID,
			Issuer:         issuerID,
			CredentialType: credType,
			Expiration:     &exp,
			Flags:          LsfCredentialAccepted,
			HasSubjectNode: true,
		})
		require.NoError(t, serr)
		require.NoError(t, view.Insert(credKey, credBlob))

		// The subject account exists; the issuer account is intentionally absent
		// so deleteSLE's owner-account existence check fails with tecINTERNAL.
		subjAcct := &state.AccountRoot{Account: subjectAddr, Balance: 100_000_000, Sequence: 1}
		subjBlob, aerr := state.SerializeAccountRoot(subjAcct)
		require.NoError(t, aerr)
		require.NoError(t, view.Insert(keylet.Account(subjectID), subjBlob))

		return &tx.ApplyContext{
			View:      view,
			Account:   subjAcct,
			AccountID: subjectID,
			Config: tx.EngineConfig{
				Rules:           rules,
				ParentCloseTime: closeTime,
			},
			Metadata: &tx.Metadata{},
			Log:      xrpllog.Discard(),
			Ctx:      context.Background(),
		}
	}

	// fixCleanup3_1_3 is supported-by-default, so the off arm must disable it
	// explicitly rather than rely on the preset lacking it.
	fix313On := amendment.NewRulesBuilder().
		FromPreset(amendment.PresetAllSupported).
		Enable(amendment.FeatureID("fixCleanup3_1_3")).
		Build()
	fix313Off := amendment.NewRulesBuilder().
		FromPreset(amendment.PresetAllSupported).
		Disable(amendment.FeatureID("fixCleanup3_1_3")).
		Build()

	t.Run("before fix: deletion failure swallowed, tecEXPIRED", func(t *testing.T) {
		ctx := buildCtx(fix313Off)
		result := VerifyDepositPreauth(ctx, []string{credIDHex}, subjectID, [20]byte{}, nil)
		require.Equal(t, ter.TecEXPIRED, result)
	})

	t.Run("after fix: deletion failure aborts, tecINTERNAL", func(t *testing.T) {
		ctx := buildCtx(fix313On)
		result := VerifyDepositPreauth(ctx, []string{credIDHex}, subjectID, [20]byte{}, nil)
		require.Equal(t, ter.TecINTERNAL, result)
	})
}

func TestDeleteSLEMissingEntry(t *testing.T) {
	require.Equal(t, ter.TecNO_ENTRY, DeleteSLE(nil, keylet.Keylet{}, nil))
}

func TestVerifyValidDomainDeletesExpiredCredential(t *testing.T) {
	view := newMapView()
	var subject, domainOwner [20]byte
	subject[19] = 1
	domainOwner[19] = 2
	domainID := [32]byte{3}
	credentialType := []byte("KYC")
	credentialKey := keylet.Credential(subject, subject, credentialType)

	account := &state.AccountRoot{
		Account:    state.EncodeAccountIDSafe(subject),
		Balance:    1_000_000_000,
		Sequence:   1,
		OwnerCount: 1,
	}
	accountRaw, err := state.SerializeAccountRoot(account)
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.Account(subject), accountRaw))

	dir, err := state.DirInsert(view, keylet.OwnerDir(subject), credentialKey.Key, false, func(node *state.DirectoryNode) {
		node.Owner = subject
	})
	require.NoError(t, err)
	expiration := uint32(100)
	credentialRaw, err := serializeCredentialEntry(&CredentialEntry{
		Subject:        subject,
		Issuer:         subject,
		CredentialType: credentialType,
		Expiration:     &expiration,
		Flags:          LsfCredentialAccepted,
		IssuerNode:     dir.Page,
	})
	require.NoError(t, err)
	require.NoError(t, view.Insert(credentialKey, credentialRaw))

	domainRaw, err := state.SerializePermissionedDomain(&state.PermissionedDomainData{
		Owner:    domainOwner,
		Sequence: 1,
		AcceptedCredentials: []state.PermissionedDomainCredential{{
			Issuer: subject, CredentialType: credentialType,
		}},
	}, state.EncodeAccountIDSafe(domainOwner))
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.PermissionedDomainByID(domainID), domainRaw))

	const closeTime = uint32(101)
	require.Equal(t, ter.TecEXPIRED, ValidDomain(view, domainID, subject, closeTime))
	exists, err := view.Exists(credentialKey)
	require.NoError(t, err)
	require.True(t, exists, "read-only validation must not delete expired credentials")

	ctx := &tx.ApplyContext{
		View:      view,
		Account:   account,
		AccountID: subject,
		Config: tx.EngineConfig{
			Rules:           amendment.AllSupportedRules(),
			ParentCloseTime: closeTime,
		},
		Metadata: &tx.Metadata{},
		Log:      xrpllog.Discard(),
		Ctx:      context.Background(),
	}
	require.Equal(t, ter.TecEXPIRED, VerifyValidDomain(ctx, subject, domainID))
	exists, err = view.Exists(credentialKey)
	require.NoError(t, err)
	require.False(t, exists)
	require.Zero(t, ctx.Account.OwnerCount)
}

func TestVerifyValidDomainDeletionFailureAmendmentArms(t *testing.T) {
	var subject, issuer [20]byte
	subject[19] = 1
	issuer[19] = 2
	domainID := [32]byte{3}
	credentialType := []byte("KYC")
	credentialKey := keylet.Credential(subject, issuer, credentialType)

	build := func(rules *amendment.Rules) (*tx.ApplyContext, *mapView) {
		view := newMapView()
		account := &state.AccountRoot{
			Account:  state.EncodeAccountIDSafe(subject),
			Balance:  1_000_000_000,
			Sequence: 1,
		}
		accountRaw, err := state.SerializeAccountRoot(account)
		require.NoError(t, err)
		require.NoError(t, view.Insert(keylet.Account(subject), accountRaw))

		expiration := uint32(100)
		credentialRaw, err := serializeCredentialEntry(&CredentialEntry{
			Subject:        subject,
			Issuer:         issuer,
			CredentialType: credentialType,
			Expiration:     &expiration,
			Flags:          LsfCredentialAccepted,
			HasSubjectNode: true,
		})
		require.NoError(t, err)
		require.NoError(t, view.Insert(credentialKey, credentialRaw))

		domainRaw, err := state.SerializePermissionedDomain(&state.PermissionedDomainData{
			Owner:    subject,
			Sequence: 1,
			AcceptedCredentials: []state.PermissionedDomainCredential{{
				Issuer: issuer, CredentialType: credentialType,
			}},
		}, state.EncodeAccountIDSafe(subject))
		require.NoError(t, err)
		require.NoError(t, view.Insert(keylet.PermissionedDomainByID(domainID), domainRaw))

		return &tx.ApplyContext{
			View:      view,
			Account:   account,
			AccountID: subject,
			Config: tx.EngineConfig{
				Rules:           rules,
				ParentCloseTime: 101,
			},
			Metadata: &tx.Metadata{},
			Log:      xrpllog.Discard(),
			Ctx:      context.Background(),
		}, view
	}

	fixOn := amendment.NewRulesBuilder().
		FromPreset(amendment.PresetAllSupported).
		Enable(amendment.FeatureFixCleanup3_1_3).
		Build()
	fixOff := amendment.NewRulesBuilder().
		FromPreset(amendment.PresetAllSupported).
		Disable(amendment.FeatureFixCleanup3_1_3).
		Build()

	tests := []struct {
		name  string
		rules *amendment.Rules
		want  ter.Result
	}{
		{name: "before fix", rules: fixOff, want: ter.TesSUCCESS},
		{name: "after fix", rules: fixOn, want: ter.TecINTERNAL},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, view := build(test.rules)
			require.Equal(t, test.want, VerifyValidDomain(ctx, subject, domainID))
			exists, err := view.Exists(credentialKey)
			require.NoError(t, err)
			require.True(t, exists)
		})
	}
}
