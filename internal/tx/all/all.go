// Package all aggregates all transaction sub-packages and exposes a single
// RegisterAll() entry point that registers every transaction type with the
// tx registry.
//
// Callers (the server binary, replay tooling, test environments) must invoke
// RegisterAll() exactly once at startup before any code that depends on the
// registry runs. RegisterAll uses sync.Once internally so it is safe to call
// from multiple test packages.
package all

import (
	"sync"

	"github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/internal/tx/amm"
	"github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/internal/tx/check"
	"github.com/LeJamon/go-xrpl/internal/tx/clawback"
	"github.com/LeJamon/go-xrpl/internal/tx/credential"
	"github.com/LeJamon/go-xrpl/internal/tx/delegate"
	"github.com/LeJamon/go-xrpl/internal/tx/depositpreauth"
	"github.com/LeJamon/go-xrpl/internal/tx/did"
	"github.com/LeJamon/go-xrpl/internal/tx/escrow"
	"github.com/LeJamon/go-xrpl/internal/tx/ledgerstatefix"
	"github.com/LeJamon/go-xrpl/internal/tx/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx/nftoken"
	"github.com/LeJamon/go-xrpl/internal/tx/offer"
	"github.com/LeJamon/go-xrpl/internal/tx/oracle"
	"github.com/LeJamon/go-xrpl/internal/tx/paychan"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/permissioneddomain"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/internal/tx/signerlist"
	"github.com/LeJamon/go-xrpl/internal/tx/ticket"
	"github.com/LeJamon/go-xrpl/internal/tx/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/internal/tx/xchain"
)

var registerOnce sync.Once

// RegisterAll registers every transaction type with the tx registry.
// It is safe (and cheap) to call multiple times: subsequent calls are no-ops.
func RegisterAll() {
	registerOnce.Do(func() {
		account.Register()
		amm.Register()
		batch.Register()
		check.Register()
		clawback.Register()
		credential.Register()
		delegate.Register()
		depositpreauth.Register()
		did.Register()
		escrow.Register()
		ledgerstatefix.Register()
		mpt.Register()
		nftoken.Register()
		offer.Register()
		oracle.Register()
		paychan.Register()
		payment.Register()
		permissioneddomain.Register()
		pseudo.Register()
		signerlist.Register()
		ticket.Register()
		trustset.Register()
		vault.Register()
		xchain.Register()
	})
}
