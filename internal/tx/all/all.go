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

	"github.com/LeJamon/goXRPLd/internal/tx/account"
	"github.com/LeJamon/goXRPLd/internal/tx/amm"
	"github.com/LeJamon/goXRPLd/internal/tx/batch"
	"github.com/LeJamon/goXRPLd/internal/tx/check"
	"github.com/LeJamon/goXRPLd/internal/tx/clawback"
	"github.com/LeJamon/goXRPLd/internal/tx/credential"
	"github.com/LeJamon/goXRPLd/internal/tx/delegate"
	"github.com/LeJamon/goXRPLd/internal/tx/depositpreauth"
	"github.com/LeJamon/goXRPLd/internal/tx/did"
	"github.com/LeJamon/goXRPLd/internal/tx/escrow"
	"github.com/LeJamon/goXRPLd/internal/tx/ledgerstatefix"
	"github.com/LeJamon/goXRPLd/internal/tx/mpt"
	"github.com/LeJamon/goXRPLd/internal/tx/nftoken"
	"github.com/LeJamon/goXRPLd/internal/tx/offer"
	"github.com/LeJamon/goXRPLd/internal/tx/oracle"
	"github.com/LeJamon/goXRPLd/internal/tx/paychan"
	"github.com/LeJamon/goXRPLd/internal/tx/payment"
	"github.com/LeJamon/goXRPLd/internal/tx/permissioneddomain"
	"github.com/LeJamon/goXRPLd/internal/tx/pseudo"
	"github.com/LeJamon/goXRPLd/internal/tx/signerlist"
	"github.com/LeJamon/goXRPLd/internal/tx/ticket"
	"github.com/LeJamon/goXRPLd/internal/tx/trustset"
	"github.com/LeJamon/goXRPLd/internal/tx/vault"
	"github.com/LeJamon/goXRPLd/internal/tx/xchain"
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
