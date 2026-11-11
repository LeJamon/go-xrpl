package invariants

import (
	"os"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/internal/tx/amm"
	"github.com/LeJamon/go-xrpl/internal/tx/check"
	"github.com/LeJamon/go-xrpl/internal/tx/clawback"
	"github.com/LeJamon/go-xrpl/internal/tx/credential"
	"github.com/LeJamon/go-xrpl/internal/tx/delegate"
	"github.com/LeJamon/go-xrpl/internal/tx/depositpreauth"
	"github.com/LeJamon/go-xrpl/internal/tx/did"
	"github.com/LeJamon/go-xrpl/internal/tx/escrow"
	"github.com/LeJamon/go-xrpl/internal/tx/ledgerstatefix"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/internal/tx/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx/nftoken"
	"github.com/LeJamon/go-xrpl/internal/tx/offer"
	"github.com/LeJamon/go-xrpl/internal/tx/oracle"
	"github.com/LeJamon/go-xrpl/internal/tx/paychan"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/permissioneddomain"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/internal/tx/signerlist"
	"github.com/LeJamon/go-xrpl/internal/tx/sponsor"
	"github.com/LeJamon/go-xrpl/internal/tx/ticket"
	"github.com/LeJamon/go-xrpl/internal/tx/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/internal/tx/xchain"
)

// TestMain registers the transaction types whose descriptors carry the
// privileges these checks consult. all.RegisterAll cannot be used here: the
// batch package depends on this one. Batch declares no privileges.
func TestMain(m *testing.M) {
	for _, register := range []func(){
		account.Register, amm.Register, check.Register, clawback.Register,
		credential.Register, delegate.Register, depositpreauth.Register,
		did.Register, escrow.Register, ledgerstatefix.Register, lending.Register,
		mpt.Register, nftoken.Register, offer.Register, oracle.Register,
		paychan.Register, payment.Register, permissioneddomain.Register,
		pseudo.Register, signerlist.Register, sponsor.Register, ticket.Register,
		trustset.Register, vault.Register, xchain.Register,
	} {
		register()
	}
	os.Exit(m.Run())
}
