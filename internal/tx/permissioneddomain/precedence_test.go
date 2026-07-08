package permissioneddomain

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

// TestPermissionedDomainSet_CheckArrayBeforeDomainID pins finding
// PermissionedDomainSet-order: rippled runs credentials::checkArray before the
// DomainID present-and-zero check, so an empty AcceptedCredentials array wins
// (temARRAY_EMPTY) even when DomainID is also zero (which alone would be
// temMALFORMED).
func TestPermissionedDomainSet_CheckArrayBeforeDomainID(t *testing.T) {
	pd := NewPermissionedDomainSet("rOwner")
	pd.DomainID = strings.Repeat("00", 32) // zero DomainID -> temMALFORMED if reached first
	pd.AcceptedCredentials = nil           // empty -> temARRAY_EMPTY
	pd.Common.Fee = "10"
	seq := uint32(1)
	pd.Common.Sequence = &seq

	err := pd.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "temARRAY_EMPTY")
}

// TestPermissionedDomainSet_ZeroIssuer pins finding
// PermissionedDomainSet-zero-issuer: a zero (all-0x00) issuer AccountID is
// temINVALID_ACCOUNT_ID in checkArray, not the tecNO_ISSUER an included tx would
// later report.
func TestPermissionedDomainSet_ZeroIssuer(t *testing.T) {
	pd := NewPermissionedDomainSet("rOwner")
	pd.AddAcceptedCredential(protocol.ZeroAccount, makeCredTypeHex(4))
	pd.Common.Fee = "10"
	seq := uint32(1)
	pd.Common.Sequence = &seq

	err := pd.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "temINVALID_ACCOUNT_ID")
}

// TestPermissionedDomainSet_DuplicateCredentialHexCase pins finding
// PermissionedDomainSet-dedup: duplicate detection operates on decoded bytes, so
// two credentials with the same issuer whose CredentialType differs only in hex
// case ("AB" vs "ab") still collide -> temMALFORMED.
func TestPermissionedDomainSet_DuplicateCredentialHexCase(t *testing.T) {
	pd := NewPermissionedDomainSet("rOwner")
	pd.AddAcceptedCredential("rIssuer1", "AB")
	pd.AddAcceptedCredential("rIssuer1", "ab")
	pd.Common.Fee = "10"
	seq := uint32(1)
	pd.Common.Sequence = &seq

	err := pd.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "temMALFORMED")
}
