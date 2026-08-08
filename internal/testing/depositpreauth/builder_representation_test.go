package depositpreauth

import (
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/stretchr/testify/require"
)

func TestCredentialTypeBuildersUseExplicitRepresentations(t *testing.T) {
	owner := jtx.NewAccount("owner")
	issuer := jtx.NewAccount("issuer")

	text := AuthCredentials(owner, []AuthorizeCredentials{
		AuthorizeCredentialText(issuer, "deadbeef"),
	}).Build()
	require.Equal(t, "6465616462656566", text.AuthorizeCredentials[0].Credential.CredentialType)

	wireCredential, err := AuthorizeCredentialHex(issuer, "DeAdBeEf")
	require.NoError(t, err)
	wire := AuthCredentials(owner, []AuthorizeCredentials{wireCredential}).Build()
	require.Equal(t, "DeAdBeEf", wire.AuthorizeCredentials[0].Credential.CredentialType)

	_, err = AuthorizeCredentialHex(issuer, "abc")
	require.Error(t, err)
	_, err = AuthorizeCredentialHex(issuer, "not-hex")
	require.Error(t, err)

	require.Error(t, AuthCredentials(owner, []AuthorizeCredentials{
		AuthorizeCredentialText(issuer, ""),
	}).Build().Validate())
	require.NoError(t, AuthCredentials(owner, []AuthorizeCredentials{
		AuthorizeCredentialText(issuer, strings.Repeat("x", 64)),
	}).Build().Validate())
	require.Error(t, AuthCredentials(owner, []AuthorizeCredentials{
		AuthorizeCredentialText(issuer, strings.Repeat("x", 65)),
	}).Build().Validate())
}

func TestAllDepositPreauthBuildersSupportTickets(t *testing.T) {
	owner := jtx.NewAccount("owner")
	target := jtx.NewAccount("target")
	credential := AuthorizeCredentialText(target, "credential")

	transactions := []*Builder{
		Auth(owner, target),
		Unauth(owner, target),
		AuthCredentials(owner, []AuthorizeCredentials{credential}),
		UnauthCredentials(owner, []AuthorizeCredentials{credential}),
	}
	for _, builder := range transactions {
		txn := builder.TicketSeq(123).Build()
		require.NotNil(t, txn.Sequence)
		require.Zero(t, *txn.Sequence)
		require.NotNil(t, txn.TicketSequence)
		require.Equal(t, uint32(123), *txn.TicketSequence)
	}
}
