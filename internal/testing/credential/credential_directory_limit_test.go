package credential_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/credential"
	"github.com/LeJamon/go-xrpl/internal/testing/ticket"
)

func TestCredentialCreateDirectorySaturationIsAtomic(t *testing.T) {
	for _, fixEnabled := range []bool{false, true} {
		for _, fullOwner := range []string{"issuer", "subject"} {
			t.Run(fmt.Sprintf("fixDirectoryLimit=%t/%s", fixEnabled, fullOwner), func(t *testing.T) {
				issuer := jtx.NewAccount("issuer")
				subject := jtx.NewAccount("subject")
				env := jtx.NewTestEnv(t)
				env.FundAmount(issuer, uint64(jtx.XRP(10_000)))
				env.FundAmount(subject, uint64(jtx.XRP(10_000)))
				env.Close()
				if !fixEnabled {
					env.DisableFeature("fixDirectoryLimit")
					env.Close()
				}

				owner := issuer
				if fullOwner == "subject" {
					owner = subject
				}
				jtx.RequireTxSuccess(t, env.Submit(ticket.TicketCreate(owner, 64).Build()))
				env.Close()
				targetPage := state.DirNodeMaxPages - 1
				if fixEnabled {
					targetPage = math.MaxUint64
				}
				if err := env.BumpDirectoryLastPage(owner, targetPage, "OwnerNode"); err != nil {
					t.Fatalf("saturate %s directory: %v", fullOwner, err)
				}

				issuerBalance := env.Balance(issuer)
				subjectBalance := env.Balance(subject)
				issuerSequence := env.Seq(issuer)
				subjectSequence := env.Seq(subject)
				issuerOwnerCount := env.OwnerCount(issuer)
				subjectOwnerCount := env.OwnerCount(subject)
				credentialKey := jtx.CredentialKeylet(subject, issuer, "full")

				result := env.Submit(credential.CredentialCreateText(issuer, subject, "full").Build())
				jtx.RequireTxClaimed(t, result, jtx.TecDIR_FULL)
				env.Close()

				jtx.RequireLedgerEntryNotExists(t, env, credentialKey)
				jtx.RequireOwnerDirectoryContains(t, env, issuer, credentialKey.Key, false)
				jtx.RequireOwnerDirectoryContains(t, env, subject, credentialKey.Key, false)
				jtx.RequireOwnerCount(t, env, issuer, issuerOwnerCount)
				jtx.RequireOwnerCount(t, env, subject, subjectOwnerCount)
				jtx.RequireBalance(t, env, issuer, issuerBalance-env.BaseFee())
				jtx.RequireBalance(t, env, subject, subjectBalance)
				jtx.RequireSequence(t, env, issuer, issuerSequence+1)
				jtx.RequireSequence(t, env, subject, subjectSequence)
			})
		}
	}
}
