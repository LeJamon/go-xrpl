package credential

import (
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/stretchr/testify/require"
)

func TestCredentialTypeBuildersUseExplicitRepresentations(t *testing.T) {
	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")

	text := CredentialCreateText(issuer, subject, "deadbeef").Build()
	require.Equal(t, "6465616462656566", text.CredentialType)

	wire := CredentialCreateHex(issuer, subject, "DeAdBeEf").Build()
	require.Equal(t, "DeAdBeEf", wire.CredentialType)

	require.Error(t, CredentialCreateHex(issuer, subject, "abc").Build().Validate())
	require.Error(t, CredentialCreateHex(issuer, subject, "not-hex").Build().Validate())
	require.Error(t, CredentialCreateText(issuer, subject, "").Build().Validate())
	require.NoError(t, CredentialCreateText(issuer, subject, strings.Repeat("x", 64)).Build().Validate())
	require.Error(t, CredentialCreateText(issuer, subject, strings.Repeat("x", 65)).Build().Validate())
}
