package credential

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

// rulesFixFlags builds a minimal Rules set with fixInvalidTxFlags on or off.
func rulesFixFlags(on bool) *amendment.Rules {
	b := amendment.NewRulesBuilder()
	if on {
		b = b.Enable(amendment.FeatureFixInvalidTxFlags)
	}
	return b.Build()
}

// TestCredentialFlagsMask pins the fixInvalidTxFlags-gated mask for all three
// Credential types. rippled's getFlagsMask is `fixInvalidTxFlags ? tfUniversalMask
// : 0`, enforced at preflight0. The mask value and gate already matched rippled;
// this locks in that the check is now GetFlagsMask (preflight0) rather than the
// first statement of Apply(), so it fires before the type's field checks and
// before signature verification.
func TestCredentialFlagsMask(t *testing.T) {
	on := rulesFixFlags(true)
	off := rulesFixFlags(false)

	require.Equal(t, tx.TfUniversalMask, (&CredentialCreate{}).GetFlagsMask(on))
	require.Equal(t, uint32(0), (&CredentialCreate{}).GetFlagsMask(off))

	require.Equal(t, tx.TfUniversalMask, (&CredentialAccept{}).GetFlagsMask(on))
	require.Equal(t, uint32(0), (&CredentialAccept{}).GetFlagsMask(off))

	require.Equal(t, tx.TfUniversalMask, (&CredentialDelete{}).GetFlagsMask(on))
	require.Equal(t, uint32(0), (&CredentialDelete{}).GetFlagsMask(off))
}

// validateCode runs Validate and returns the mapped TER code.
func validateCode(t *testing.T, err error) ter.Result {
	t.Helper()
	require.Error(t, err)
	var re *ter.ResultError
	require.True(t, errors.As(err, &re), "expected a typed ResultError, got %v", err)
	return re.Code
}

// TestCredentialDeletePresentEmptyAccountZeroed pins that a present but
// zero-length sfSubject / sfIssuer is rejected temINVALID_ACCOUNT_ID. The binary
// codec decodes a present zero-length STAccount to "", which rippled treats as a
// present account that isZero() -> temINVALID_ACCOUNT_ID. Before the fix goXRPL
// only ran the zero check on a non-empty decoded string, so a present-empty
// field slipped through Validate and Apply defaulted it to the sender.
func TestCredentialDeletePresentEmptyAccountZeroed(t *testing.T) {
	t.Run("Subject present but empty", func(t *testing.T) {
		c := NewCredentialDelete("rSenderAccountForTestingOnly000000", "ABCD")
		c.SetPresentFields(map[string]bool{"Subject": true})
		require.Equal(t, ter.TemINVALID_ACCOUNT_ID, validateCode(t, c.Validate()))
	})

	t.Run("Issuer present but empty", func(t *testing.T) {
		c := NewCredentialDelete("rSenderAccountForTestingOnly000000", "ABCD")
		c.SetPresentFields(map[string]bool{"Issuer": true})
		require.Equal(t, ter.TemINVALID_ACCOUNT_ID, validateCode(t, c.Validate()))
	})

	t.Run("neither present is malformed not zeroed", func(t *testing.T) {
		c := NewCredentialDelete("rSenderAccountForTestingOnly000000", "ABCD")
		require.Equal(t, ter.TemMALFORMED, validateCode(t, c.Validate()))
	})
}
