// Package all imports all transaction sub-packages to trigger their init() registrations.
// Import this package in the main application to ensure all transaction types are registered.
//TODO This will be update for cleaner approach with less shenanigans

package all

import (
	_ "github.com/LeJamon/goXRPLd/internal/tx/account"
	_ "github.com/LeJamon/goXRPLd/internal/tx/amm"
	_ "github.com/LeJamon/goXRPLd/internal/tx/batch"
	_ "github.com/LeJamon/goXRPLd/internal/tx/check"
	_ "github.com/LeJamon/goXRPLd/internal/tx/clawback"
	_ "github.com/LeJamon/goXRPLd/internal/tx/credential"
	_ "github.com/LeJamon/goXRPLd/internal/tx/delegate"
	_ "github.com/LeJamon/goXRPLd/internal/tx/depositpreauth"
	_ "github.com/LeJamon/goXRPLd/internal/tx/did"
	_ "github.com/LeJamon/goXRPLd/internal/tx/escrow"
	_ "github.com/LeJamon/goXRPLd/internal/tx/ledgerstatefix"
	_ "github.com/LeJamon/goXRPLd/internal/tx/mpt"
	_ "github.com/LeJamon/goXRPLd/internal/tx/nftoken"
	_ "github.com/LeJamon/goXRPLd/internal/tx/offer"
	_ "github.com/LeJamon/goXRPLd/internal/tx/oracle"
	_ "github.com/LeJamon/goXRPLd/internal/tx/paychan"
	_ "github.com/LeJamon/goXRPLd/internal/tx/payment"
	_ "github.com/LeJamon/goXRPLd/internal/tx/permissioneddomain"
	_ "github.com/LeJamon/goXRPLd/internal/tx/pseudo"
	_ "github.com/LeJamon/goXRPLd/internal/tx/signerlist"
	_ "github.com/LeJamon/goXRPLd/internal/tx/ticket"
	_ "github.com/LeJamon/goXRPLd/internal/tx/trustset"
	_ "github.com/LeJamon/goXRPLd/internal/tx/vault"
	_ "github.com/LeJamon/goXRPLd/internal/tx/xchain"
)
