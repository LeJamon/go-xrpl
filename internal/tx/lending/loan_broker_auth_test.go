package lending

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

type coverAuthFixture struct {
	base   *coverClawbackFixture
	owner  [20]byte
	dest   [20]byte
	amount tx.Amount
	broker string
}

func newCoverAuthFixture(t *testing.T) *coverAuthFixture {
	t.Helper()
	base := newCoverClawbackFixture(t, 0)
	owner := repeatedAccountID(0x34)
	dest := repeatedAccountID(0x35)

	brokerKey := keylet.LoanBrokerByID(base.brokerID)
	broker, err := readLoanBroker(base.view, brokerKey)
	if err != nil || broker == nil {
		t.Fatalf("read broker: broker=%v err=%v", broker, err)
	}
	broker.Owner = owner
	brokerBytes, err := serializeLoanBrokerForRules(broker, base.view.rules)
	if err != nil {
		t.Fatalf("serialize broker: %v", err)
	}
	base.view.data[brokerKey.Key] = brokerBytes

	putAccount := func(id [20]byte) {
		t.Helper()
		raw, err := state.SerializeAccountRoot(&state.AccountRoot{
			Account:  encodeTestAccount(t, id),
			Balance:  1_000_000_000,
			Sequence: 1,
		})
		if err != nil {
			t.Fatalf("serialize account: %v", err)
		}
		base.view.data[keylet.Account(id).Key] = raw
	}
	putAccount(base.issuerID)
	putAccount(owner)
	putAccount(dest)

	issuer := encodeTestAccount(t, base.issuerID)
	amount := state.NewMPTAmountWithIssuanceID(
		100,
		issuer,
		hex.EncodeToString(base.mptID[:]),
	)
	return &coverAuthFixture{
		base:   base,
		owner:  owner,
		dest:   dest,
		amount: amount,
		broker: strings.ToUpper(hex.EncodeToString(base.brokerID[:])),
	}
}

func (f *coverAuthFixture) putHolding(t *testing.T, account [20]byte) {
	t.Helper()
	raw, err := state.SerializeMPToken(&state.MPTokenData{
		Account:           account,
		MPTokenIssuanceID: f.base.mptID,
		MPTAmount:         10_000,
	})
	if err != nil {
		t.Fatalf("serialize holding: %v", err)
	}
	f.base.view.data[keylet.MPTokenByID(f.base.mptID, account).Key] = raw
}

func (f *coverAuthFixture) ownerAddress(t *testing.T) string {
	t.Helper()
	return encodeTestAccount(t, f.owner)
}

func (f *coverAuthFixture) destinationAddress(t *testing.T) string {
	t.Helper()
	return encodeTestAccount(t, f.dest)
}

func TestLoanBrokerCoverDepositRequiresMPTHolding(t *testing.T) {
	fixture := newCoverAuthFixture(t)
	deposit := NewLoanBrokerCoverDeposit(fixture.ownerAddress(t), fixture.broker, fixture.amount)

	if got := deposit.Preclaim(fixture.base.view, fixture.base.config); got != ter.TecNO_AUTH {
		t.Fatalf("missing depositor holding: got %v, want tecNO_AUTH", got)
	}

	fixture.putHolding(t, fixture.owner)
	if got := deposit.Preclaim(fixture.base.view, fixture.base.config); got != ter.TesSUCCESS {
		t.Fatalf("existing depositor holding: got %v, want tesSUCCESS", got)
	}
}

func TestLoanBrokerCoverWithdrawAuthModeDependsOnDestination(t *testing.T) {
	t.Run("self withdrawal uses weak auth", func(t *testing.T) {
		fixture := newCoverAuthFixture(t)
		withdraw := NewLoanBrokerCoverWithdraw(fixture.ownerAddress(t), fixture.broker, fixture.amount)

		if got := withdraw.Preclaim(fixture.base.view, fixture.base.config); got != ter.TesSUCCESS {
			t.Fatalf("missing self holding: got %v, want tesSUCCESS", got)
		}
	})

	t.Run("third-party withdrawal uses strong auth", func(t *testing.T) {
		fixture := newCoverAuthFixture(t)
		withdraw := NewLoanBrokerCoverWithdraw(fixture.ownerAddress(t), fixture.broker, fixture.amount)
		withdraw.Destination = fixture.destinationAddress(t)

		if got := withdraw.Preclaim(fixture.base.view, fixture.base.config); got != ter.TecNO_AUTH {
			t.Fatalf("missing destination holding: got %v, want tecNO_AUTH", got)
		}

		fixture.putHolding(t, fixture.dest)
		if got := withdraw.Preclaim(fixture.base.view, fixture.base.config); got != ter.TesSUCCESS {
			t.Fatalf("existing destination holding: got %v, want tesSUCCESS", got)
		}
	})
}
