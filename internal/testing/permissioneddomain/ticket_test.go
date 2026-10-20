package permissioneddomain_test

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	pd "github.com/LeJamon/go-xrpl/internal/testing/permissioneddomain"
	"github.com/LeJamon/go-xrpl/internal/testing/ticket"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestDomainSetTickets(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		name := "legacy"
		if fixed {
			name = "fixCleanup3_1_3"
		}
		t.Run(name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			if !fixed {
				env.DisableFeature("fixCleanup3_1_3")
			}
			alice := jtx.NewAccount("alice")
			env.Fund(alice)
			firstTicket := env.Seq(alice) + 1
			jtx.RequireTxSuccess(t, env.Submit(ticket.TicketCreate(alice, 2).Build()))
			env.Close()
			sequence := env.Seq(alice)

			create := pd.DomainSet(alice).Credential(alice, "63726564656E7469616C31").Build()
			jtx.RequireTxSuccess(t, env.Submit(jtx.WithTicketSeq(create, firstTicket)))
			env.Close()
			domainSeq := uint32(0)
			if fixed {
				domainSeq = firstTicket
			}
			domainKey := keylet.PermissionedDomain(alice.ID, domainSeq)
			data, err := env.LedgerEntry(domainKey)
			if err != nil || data == nil {
				t.Fatalf("domain for sequence %d missing: %v", domainSeq, err)
			}
			domain, err := state.ParsePermissionedDomain(data)
			if err != nil || domain.Sequence != domainSeq {
				t.Fatalf("domain sequence: %+v, error: %v", domain, err)
			}
			jtx.RequireTicketCount(t, env, alice, 1)
			jtx.RequireOwnerCount(t, env, alice, 2)

			beforeBalance := env.Balance(alice)
			beforeDir, err := env.LedgerEntry(keylet.OwnerDir(alice.ID))
			if err != nil {
				t.Fatal(err)
			}
			create = pd.DomainSet(alice).Credential(alice, "63726564656E7469616C31").Build()
			result := env.Submit(jtx.WithTicketSeq(create, firstTicket+1))
			if fixed {
				jtx.RequireTxSuccess(t, result)
				jtx.RequireTicketCount(t, env, alice, 0)
				jtx.RequireBalance(t, env, alice, beforeBalance-env.BaseFee())
				if second := getDomainEntry(t, env, keylet.PermissionedDomain(alice.ID, firstTicket+1)); second == nil {
					t.Fatal("second ticket did not create its own domain")
				}
			} else {
				jtx.RequireTxFail(t, result, "tefEXCEPTION")
				if result.Applied || result.Fee != 0 || result.Metadata != nil {
					t.Fatalf("duplicate domain was applied: %+v", result)
				}
				jtx.RequireTicketCount(t, env, alice, 1)
				jtx.RequireBalance(t, env, alice, beforeBalance)
				afterDir, err := env.LedgerEntry(keylet.OwnerDir(alice.ID))
				if err != nil || !bytes.Equal(beforeDir, afterDir) {
					t.Fatalf("failed creation changed owner directory: %v", err)
				}
				afterDomain, err := env.LedgerEntry(domainKey)
				if err != nil || !bytes.Equal(data, afterDomain) {
					t.Fatalf("failed creation changed existing domain: %v", err)
				}
			}
			jtx.RequireOwnerCount(t, env, alice, 2)
			if env.Seq(alice) != sequence {
				t.Fatal("ticketed domain creation changed account sequence")
			}
		})
	}
}
