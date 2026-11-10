package paychan

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
)

func TestPaymentChannelCreateTicketCountsTowardReserve(t *testing.T) {
	const amount = int64(100)

	for _, tc := range []struct {
		name    string
		sponsor bool
		caseID  string
		want    string
	}{
		{name: "SponsorDisabled/Reserve", caseID: "reserve", want: "tecINSUFFICIENT_RESERVE"},
		{name: "SponsorDisabled/Funding", caseID: "funding", want: "tecUNFUNDED"},
		{name: "SponsorDisabled/Exact", caseID: "exact", want: "tesSUCCESS"},
		{name: "SponsorEnabled/Reserve", sponsor: true, caseID: "reserve", want: "tecINSUFFICIENT_RESERVE"},
		{name: "SponsorEnabled/Funding", sponsor: true, caseID: "funding", want: "tecUNFUNDED"},
		{name: "SponsorEnabled/Exact", sponsor: true, caseID: "exact", want: "tesSUCCESS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			alice := jtx.NewAccount("alice")
			bob := jtx.NewAccount("bob")

			// With Sponsor disabled, preclaim sees one ticket and the new channel;
			// with Sponsor enabled, apply sees the consumed ticket and new channel.
			ownerCount := uint64(1)
			if tc.sponsor {
				ownerCount = 0
			}
			reserve := env.ReserveBase() + (ownerCount+1)*env.ReserveIncrement()
			balance := reserve
			switch tc.caseID {
			case "reserve":
				balance--
			case "funding":
				balance += uint64(amount) - 1
			case "exact":
				balance += uint64(amount)
			default:
				t.Fatalf("unknown case %q", tc.caseID)
			}
			env.FundAmount(alice, balance+env.BaseFee())
			env.FundAmount(bob, env.ReserveBase())
			env.Close()

			if tc.sponsor {
				env.EnableFeature("Sponsor")
			} else {
				env.DisableFeature("Sponsor")
			}
			env.Close()
			if got := env.FeatureEnabled("Sponsor"); got != tc.sponsor {
				t.Fatalf("Sponsor amendment = %v, want %v after close", got, tc.sponsor)
			}

			ticketSeq := env.CreateTickets(alice, 1)
			env.Close()
			jtx.RequireOwnerCount(t, env, alice, 1)
			jtx.RequireTicketCount(t, env, alice, 1)
			preBalance := env.Balance(alice)
			preSequence := env.Seq(alice)
			channelKey := chanKeylet(alice, bob, ticketSeq)

			result := env.Submit(ChannelCreate(alice, bob, amount, 3600, alice.PublicKeyHex()).
				Ticket(ticketSeq).
				Build())
			if result.Code != tc.want {
				t.Fatalf("PaymentChannelCreate result = %s, want %s", result.Code, tc.want)
			}
			env.Close()

			if result.Code == "tesSUCCESS" {
				if result.Fee != env.BaseFee() {
					t.Fatalf("success fee = %d, want %d", result.Fee, env.BaseFee())
				}
				jtx.RequireBalance(t, env, alice, preBalance-result.Fee-uint64(amount))
				jtx.RequireSequence(t, env, alice, preSequence)
				jtx.RequireOwnerCount(t, env, alice, 1)
				jtx.RequireTicketCount(t, env, alice, 0)
				if !chanExists(env, channelKey) {
					t.Fatal("ticketed payment channel was not created")
				}
				return
			}

			if result.Fee != env.BaseFee() {
				t.Fatalf("claimed failure fee = %d, want %d", result.Fee, env.BaseFee())
			}
			jtx.RequireBalance(t, env, alice, preBalance-result.Fee)
			jtx.RequireSequence(t, env, alice, preSequence)
			jtx.RequireOwnerCount(t, env, alice, 0)
			jtx.RequireTicketCount(t, env, alice, 0)
			if chanExists(env, channelKey) {
				t.Fatal("payment channel was created after a claimed failure")
			}
		})
	}
}

func TestPaymentChannelCreateSequenceReserveControl(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.DisableFeature("Sponsor")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	amount := int64(100)
	env.FundAmount(alice, env.ReserveBase()+env.ReserveIncrement()+uint64(amount)+env.BaseFee())
	env.FundAmount(bob, env.ReserveBase())
	env.Close()
	env.Close()
	if env.FeatureEnabled("Sponsor") {
		t.Fatal("Sponsor amendment remained enabled after disable and close")
	}

	sequence := env.Seq(alice)
	channelKey := chanKeylet(alice, bob, sequence)
	preBalance := env.Balance(alice)
	result := env.Submit(ChannelCreate(alice, bob, amount, 3600, alice.PublicKeyHex()).Build())
	if result.Code != "tesSUCCESS" {
		t.Fatalf("sequence PaymentChannelCreate result = %s, want tesSUCCESS", result.Code)
	}
	env.Close()
	jtx.RequireBalance(t, env, alice, preBalance-result.Fee-uint64(amount))
	jtx.RequireSequence(t, env, alice, sequence+1)
	jtx.RequireOwnerCount(t, env, alice, 1)
	jtx.RequireTicketCount(t, env, alice, 0)
	if !chanExists(env, channelKey) {
		t.Fatal("sequence payment channel was not created")
	}
}
