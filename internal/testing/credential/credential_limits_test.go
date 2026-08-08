package credential_test

import (
	"fmt"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/credential"
)

func TestCredentialTypeRawByteBoundaries(t *testing.T) {
	for _, operation := range []string{"create", "accept", "delete"} {
		for _, size := range []int{0, 1, 64, 65} {
			t.Run(fmt.Sprintf("%s/%d", operation, size), func(t *testing.T) {
				issuer := jtx.NewAccount("issuer")
				subject := jtx.NewAccount("subject")
				env := jtx.NewTestEnv(t)
				env.Fund(issuer, subject)
				env.Close()

				credentialType := make([]byte, size)
				for i := range credentialType {
					credentialType[i] = byte(i + 1)
				}
				if operation != "create" && size > 0 && size <= 64 {
					jtx.RequireTxSuccess(t, env.Submit(
						credential.CredentialCreateBytes(issuer, subject, credentialType).Build(),
					))
					env.Close()
				}

				var result jtx.TxResult
				switch operation {
				case "create":
					result = env.Submit(credential.CredentialCreateBytes(issuer, subject, credentialType).Build())
				case "accept":
					result = env.Submit(credential.CredentialAcceptBytes(subject, issuer, credentialType).Build())
				case "delete":
					result = env.Submit(credential.CredentialDeleteBytes(issuer, subject, issuer, credentialType).Build())
				}

				if size == 0 || size > 64 {
					jtx.RequireTxFail(t, result, jtx.TemMALFORMED)
				} else {
					jtx.RequireTxSuccess(t, result)
				}
			})
		}
	}
}

func TestCredentialURIRawByteBoundaries(t *testing.T) {
	for _, size := range []int{0, 1, 256, 257} {
		t.Run(fmt.Sprintf("%d", size), func(t *testing.T) {
			issuer := jtx.NewAccount("issuer")
			subject := jtx.NewAccount("subject")
			env := jtx.NewTestEnv(t)
			env.Fund(issuer, subject)
			env.Close()

			uri := make([]byte, size)
			for i := range uri {
				uri[i] = byte(i)
			}
			result := env.Submit(
				credential.CredentialCreateText(issuer, subject, "uri").URIHex(fmt.Sprintf("%x", uri)).Build(),
			)
			if size == 0 || size > 256 {
				jtx.RequireTxFail(t, result, jtx.TemMALFORMED)
			} else {
				jtx.RequireTxSuccess(t, result)
			}
		})
	}
}
