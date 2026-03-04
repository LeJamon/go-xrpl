// Package all imports all transaction sub-packages to trigger their init() registrations.
// Import this package in the main application to ensure all transaction types are registered.
//TODO This will be update for cleaner approach with less shenanigans

package all

import (
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/account"
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/amm"
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/batch"
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/check"
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/clawback"
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/credential"
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/delegate"
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/depositpreauth"
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/did"
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/escrow"
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/ledgerstatefix"
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/mpt"
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/nftoken"
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/offer"
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/oracle"
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/paychan"
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/payment"
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/permissioneddomain"
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/pseudo"
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/signerlist"
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/ticket"
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/trustset"
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/vault"
	_ "github.com/LeJamon/goXRPLd/internal/core/tx/xchain"
)
