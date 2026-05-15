package handlers

import "github.com/LeJamon/goXRPLd/internal/rpc/loadtrack"

// LoadKind overrides per method. Handlers that don't declare one pay
// loadtrack.LoadReference (rippled feeReferenceRPC parity). Costs are
// taken from rippled's Resource::Fees catalogue: medium = 400, heavy =
// 3000 (Resource/Fees.cpp).
//
// Heavy: anything that walks the full state map (book_offers,
// ledger_data, noripple_check) or runs pathfinding (ripple_path_find,
// path_find). account_tx scans the transaction history table.
//
// Medium: per-account ledger reads with a configurable limit. They do
// work proportional to account size; not as costly as pathfinding but
// noticeably more than a ping.

func (PathFindMethod) LoadKind() loadtrack.LoadKind       { return loadtrack.LoadHeavy }
func (RipplePathFindMethod) LoadKind() loadtrack.LoadKind { return loadtrack.LoadHeavy }
func (LedgerDataMethod) LoadKind() loadtrack.LoadKind     { return loadtrack.LoadHeavy }
func (BookOffersMethod) LoadKind() loadtrack.LoadKind     { return loadtrack.LoadHeavy }
func (NoRippleCheckMethod) LoadKind() loadtrack.LoadKind  { return loadtrack.LoadHeavy }
func (AccountTxMethod) LoadKind() loadtrack.LoadKind      { return loadtrack.LoadHeavy }

func (AccountLinesMethod) LoadKind() loadtrack.LoadKind    { return loadtrack.LoadMedium }
func (AccountObjectsMethod) LoadKind() loadtrack.LoadKind  { return loadtrack.LoadMedium }
func (AccountOffersMethod) LoadKind() loadtrack.LoadKind   { return loadtrack.LoadMedium }
func (AccountChannelsMethod) LoadKind() loadtrack.LoadKind { return loadtrack.LoadMedium }
func (AccountNftsMethod) LoadKind() loadtrack.LoadKind     { return loadtrack.LoadMedium }
func (GatewayBalancesMethod) LoadKind() loadtrack.LoadKind { return loadtrack.LoadMedium }
func (SubmitMethod) LoadKind() loadtrack.LoadKind          { return loadtrack.LoadMedium }
func (SignMethod) LoadKind() loadtrack.LoadKind            { return loadtrack.LoadMedium }
func (SignForMethod) LoadKind() loadtrack.LoadKind         { return loadtrack.LoadMedium }
