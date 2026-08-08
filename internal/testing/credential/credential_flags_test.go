package credential_test

import (
	"fmt"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/credential"
)

func TestCredentialInvalidFlagsAmendmentMatrix(t *testing.T) {
	for _, fixEnabled := range []bool{false, true} {
		fixEnabled := fixEnabled
		t.Run(fmt.Sprintf("fixInvalidTxFlags=%t", fixEnabled), func(t *testing.T) {
			for _, operation := range []string{"create", "accept", "delete"} {
				operation := operation
				t.Run(operation, func(t *testing.T) {
					issuer := jtx.NewAccount("issuer")
					subject := jtx.NewAccount("subject")
					env := jtx.NewTestEnv(t)
					env.Fund(issuer, subject)
					env.Close()
					if !fixEnabled {
						env.DisableFeature("fixInvalidTxFlags")
						env.Close()
					}

					const credentialType = "flags"
					if operation != "create" {
						jtx.RequireTxSuccess(t, env.Submit(
							credential.CredentialCreateText(issuer, subject, credentialType).Build(),
						))
						env.Close()
					}

					var result jtx.TxResult
					switch operation {
					case "create":
						result = env.Submit(credential.CredentialCreateText(issuer, subject, credentialType).Flags(1).Build())
					case "accept":
						result = env.Submit(credential.CredentialAcceptText(subject, issuer, credentialType).Flags(1).Build())
					case "delete":
						result = env.Submit(credential.CredentialDeleteText(issuer, subject, issuer, credentialType).Flags(1).Build())
					}

					if fixEnabled {
						jtx.RequireTxFail(t, result, jtx.TemINVALID_FLAG)
					} else {
						jtx.RequireTxSuccess(t, result)
					}
				})
			}
		})
	}
}
