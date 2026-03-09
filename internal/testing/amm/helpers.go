// Package amm provides test helpers and builders for AMM transaction testing.
// Reference: rippled/src/test/jtx/AMM.h and rippled/src/test/jtx/AMMTest.h
package amm

import (
	"testing"

	addresscodec "github.com/LeJamon/goXRPLd/codec/address-codec"
	"github.com/LeJamon/goXRPLd/ledger/entry"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	coreAmm "github.com/LeJamon/goXRPLd/internal/core/tx/amm"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	offerbuild "github.com/LeJamon/goXRPLd/internal/testing/offer"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/LeJamon/goXRPLd/internal/testing/trustset"
)

// AMM-specific result codes
const (
	TecAMM_BALANCE        = "tecAMM_BALANCE"
	TecAMM_FAILED         = "tecAMM_FAILED"
	TecAMM_INVALID_TOKENS = "tecAMM_INVALID_TOKENS"
	TecAMM_NOT_EMPTY      = "tecAMM_NOT_EMPTY"
	TecUNFUNDED_AMM       = "tecUNFUNDED_AMM"
	TecDUPLICATE          = "tecDUPLICATE"
	TecFROZEN             = "tecFROZEN"
	TecNO_AUTH            = "tecNO_AUTH"
	TecINSUF_RESERVE_LINE = "tecINSUF_RESERVE_LINE"
	TecNO_PERMISSION      = "tecNO_PERMISSION"
	TecAMM_EMPTY          = "tecAMM_EMPTY"
	TecINCOMPLETE         = "tecINCOMPLETE"
	TecPATH_PARTIAL       = "tecPATH_PARTIAL"

	TerNO_AMM              = "terNO_AMM"
	TerNO_ACCOUNT          = "terNO_ACCOUNT"
	TerNO_RIPPLE           = "terNO_RIPPLE"
	TerADDRESS_COLLISION   = "terADDRESS_COLLISION"

	TemBAD_AMM_TOKENS = "temBAD_AMM_TOKENS"
	TemBAD_AMOUNT     = "temBAD_AMOUNT"
	TemBAD_CURRENCY   = "temBAD_CURRENCY"
	TemBAD_FEE        = "temBAD_FEE"
	TemINVALID_FLAG   = "temINVALID_FLAG"
	TemMALFORMED      = "temMALFORMED"
	TemDISABLED       = "temDISABLED"

	TelINSUF_FEE_P = "telINSUF_FEE_P"

	TesSUCCESS = "tesSUCCESS"
)

// AMMTestEnv wraps TestEnv with AMM-specific helpers.
// Reference: rippled AMMTest base class
type AMMTestEnv struct {
	*jtx.TestEnv
	T *testing.T

	// Standard test accounts matching rippled's test environment
	GW    *jtx.Account // Gateway/issuer account
	Alice *jtx.Account
	Carol *jtx.Account
	Bob   *jtx.Account

	// Standard currencies
	USD tx.Asset
	EUR tx.Asset
	BTC tx.Asset
	GBP tx.Asset
}

// NewAMMTestEnv creates a new AMM test environment with standard accounts and currencies.
// This matches rippled's AMMTest setup.
func NewAMMTestEnv(t *testing.T) *AMMTestEnv {
	t.Helper()

	env := jtx.NewTestEnv(t)

	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")
	carol := jtx.NewAccount("carol")
	bob := jtx.NewAccount("bob")

	return &AMMTestEnv{
		TestEnv: env,
		T:       t,
		GW:      gw,
		Alice:   alice,
		Carol:   carol,
		Bob:     bob,
		USD:     tx.Asset{Currency: "USD", Issuer: gw.Address},
		EUR:     tx.Asset{Currency: "EUR", Issuer: gw.Address},
		BTC:     tx.Asset{Currency: "BTC", Issuer: gw.Address},
		GBP:     tx.Asset{Currency: "GBP", Issuer: gw.Address},
	}
}

// XRP returns the XRP asset (no issuer).
func XRP() tx.Asset {
	return tx.Asset{Currency: "XRP"}
}

// Fund funds the standard accounts (gw, alice, carol) with XRP and sets up IOUs.
// This matches rippled's fund() helper in AMM tests.
func (e *AMMTestEnv) Fund() {
	e.T.Helper()

	// Fund all accounts with 30,000 XRP
	e.TestEnv.FundAmount(e.GW, uint64(jtx.XRP(30000)))
	e.TestEnv.FundAmount(e.Alice, uint64(jtx.XRP(30000)))
	e.TestEnv.FundAmount(e.Carol, uint64(jtx.XRP(30000)))
	e.Close()
}

// FundWithIOUs funds accounts and sets up trust lines with IOUs.
// This matches rippled's fund() with Fund::All flag.
func (e *AMMTestEnv) FundWithIOUs(usdAmount, btcAmount float64) {
	e.T.Helper()

	e.Fund()

	// Set up USD trust lines
	e.Trust(e.Alice, e.GW, "USD", 100000)
	e.Trust(e.Carol, e.GW, "USD", 100000)
	e.Close()

	// Fund with USD
	if usdAmount > 0 {
		e.PayIOU(e.GW, e.Alice, "USD", usdAmount)
		e.PayIOU(e.GW, e.Carol, "USD", usdAmount)
	}
	e.Close()

	// Set up BTC trust lines if needed
	if btcAmount > 0 {
		e.Trust(e.Alice, e.GW, "BTC", 100)
		e.Trust(e.Carol, e.GW, "BTC", 100)
		e.Close()
		e.PayIOU(e.GW, e.Alice, "BTC", btcAmount)
		e.PayIOU(e.GW, e.Carol, "BTC", btcAmount)
		e.Close()
	}
}

// FundBob funds bob with XRP and optionally USD.
func (e *AMMTestEnv) FundBob(xrp int64, usd float64) {
	e.T.Helper()
	e.TestEnv.FundAmount(e.Bob, uint64(jtx.XRP(xrp)))
	if usd > 0 {
		e.Trust(e.Bob, e.GW, "USD", usd*2)
		e.Close()
		e.PayIOU(e.GW, e.Bob, "USD", usd)
	}
	e.Close()
}

// Trust creates a trust line from holder to issuer for the specified currency.
func (e *AMMTestEnv) Trust(holder, issuer *jtx.Account, currency string, limit float64) {
	e.T.Helper()

	limitAmt := tx.NewIssuedAmountFromFloat64(limit, currency, issuer.Address)
	trustTx := trustset.TrustSet(holder, limitAmt).Build()
	result := e.Submit(trustTx)
	if !result.Success {
		e.T.Fatalf("Failed to create trust line %s->%s for %s: %s", holder.Name, issuer.Name, currency, result.Code)
	}
}

// PayIOU sends an IOU payment from sender to receiver.
func (e *AMMTestEnv) PayIOU(sender, receiver *jtx.Account, currency string, amount float64) {
	e.T.Helper()

	amt := tx.NewIssuedAmountFromFloat64(amount, currency, sender.Address)
	payTx := payment.PayIssued(sender, receiver, amt).Build()
	result := e.Submit(payTx)
	if !result.Success {
		e.T.Fatalf("Failed to pay %f %s from %s to %s: %s", amount, currency, sender.Name, receiver.Name, result.Code)
	}
}

// PayIOUAmount pays a precise IOU amount (with mantissa/exponent) from sender to receiver.
func (e *AMMTestEnv) PayIOUAmount(sender, receiver *jtx.Account, amt tx.Amount) {
	e.T.Helper()

	payTx := payment.PayIssued(sender, receiver, amt).Build()
	result := e.Submit(payTx)
	if !result.Success {
		e.T.Fatalf("Failed to pay %v from %s to %s: %s", amt, sender.Name, receiver.Name, result.Code)
	}
}

// ToIOUForCalc converts an Amount to IOU representation for comparison.
// Exported wrapper around the internal toIOUForCalc.
func ToIOUForCalc(amt tx.Amount) tx.Amount {
	return coreAmm.ToIOUForCalcExported(amt)
}

// ExpectTER checks if the result matches one of the expected TER codes.
func ExpectTER(t *testing.T, result jtx.TxResult, expectedCodes ...string) {
	t.Helper()

	for _, code := range expectedCodes {
		if result.Code == code {
			return
		}
	}

	if len(expectedCodes) == 1 {
		t.Fatalf("Expected %s, got %s: %s", expectedCodes[0], result.Code, result.Message)
	} else {
		t.Fatalf("Expected one of %v, got %s: %s", expectedCodes, result.Code, result.Message)
	}
}

// XRPAmount creates an XRP amount in drops for use in transactions.
func XRPAmount(xrp int64) tx.Amount {
	return tx.NewXRPAmount(xrp * 1_000_000)
}

// IOUAmount creates an IOU amount for use in transactions.
// If issuer is nil, the amount is created with an empty issuer (use TestAMM helper to fix up).
func IOUAmount(issuer *jtx.Account, currency string, amount float64) tx.Amount {
	if issuer == nil {
		return tx.NewIssuedAmountFromFloat64(amount, currency, "")
	}
	return tx.NewIssuedAmountFromFloat64(amount, currency, issuer.Address)
}

// IOU creates an IOU amount (alias for IOUAmount).
func IOU(issuer *jtx.Account, currency string, amount float64) tx.Amount {
	return IOUAmount(issuer, currency, amount)
}

// LPTokenAmount creates an LP token amount for the given AMM asset pair.
// This generates the proper LP token currency (starting with 03) and uses the AMM account as issuer.
// Reference: rippled test fixtures use amm.lptIssue() which is the real LP token issue.
func LPTokenAmount(asset1, asset2 tx.Asset, amount float64) tx.Amount {
	lptCurrency := coreAmm.GenerateAMMLPTCurrency(asset1.Currency, asset2.Currency)
	ammAccountAddr := coreAmm.ComputeAMMAccountAddress(asset1, asset2)
	return tx.NewIssuedAmountFromFloat64(amount, lptCurrency, ammAccountAddr)
}

// TestAMMCallback is the function signature for AMM test callbacks.
// Reference: rippled's testAMM callback pattern
type TestAMMCallback func(env *AMMTestEnv, ammAcc *jtx.Account)

// TestAMM sets up an environment and AMM matching rippled's testAMM helper.
// It funds gw/alice/carol with 30,000 XRP and 30,000 of each IOU currency used,
// creates the AMM with alice, and calls the callback.
// The pool parameter defaults to XRP(10000)/USD(10000) if nil.
// Reference: rippled/src/test/jtx/impl/AMMTest.cpp testAMM()
func TestAMM(t *testing.T, pool *[2]tx.Amount, tradingFee uint16, callback TestAMMCallback) {
	t.Helper()

	var asset1, asset2 tx.Amount
	if pool != nil {
		asset1, asset2 = pool[0], pool[1]
	} else {
		// Default pool: XRP(10000)/USD(10000)
		asset1 = tx.NewXRPAmount(10000 * 1_000_000)
		asset2 = tx.NewIssuedAmountFromFloat64(10000, "USD", "")
	}

	env := NewAMMTestEnv(t)

	// Determine funding amounts — at least 30,000 of each
	xrpFund := int64(30000)
	iouFund := float64(30000)

	// Fund accounts
	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(xrpFund)))
	env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(xrpFund)))
	env.Close()

	// Set up IOU trust lines and fund
	// Gather all IOU currencies from the pool
	for _, amt := range []tx.Amount{asset1, asset2} {
		if !amt.IsNative() {
			// Fix issuer to GW if empty
			issuer := amt.Issuer
			if issuer == "" {
				issuer = env.GW.Address
			}
			env.Trust(env.Alice, env.GW, amt.Currency, iouFund*2)
			env.Trust(env.Carol, env.GW, amt.Currency, iouFund*2)
		}
	}
	env.Close()

	for _, amt := range []tx.Amount{asset1, asset2} {
		if !amt.IsNative() {
			env.PayIOU(env.GW, env.Alice, amt.Currency, iouFund)
			env.PayIOU(env.GW, env.Carol, amt.Currency, iouFund)
		}
	}
	env.Close()

	// Fix issuer for IOU amounts in pool
	if !asset1.IsNative() && asset1.Issuer == "" {
		asset1.Issuer = env.GW.Address
	}
	if !asset2.IsNative() && asset2.Issuer == "" {
		asset2.Issuer = env.GW.Address
	}

	// Create AMM with alice
	createTx := AMMCreate(env.Alice, asset1, asset2).TradingFee(tradingFee).Build()
	result := env.Submit(createTx)
	if !result.Success {
		t.Fatalf("Failed to create AMM: %s - %s", result.Code, result.Message)
	}
	env.Close()

	// Compute AMM account
	a1 := tx.Asset{Currency: asset1.Currency, Issuer: asset1.Issuer}
	a2 := tx.Asset{Currency: asset2.Currency, Issuer: asset2.Issuer}
	ammAcc := AMMAccount(t, a1, a2)

	callback(env, ammAcc)
}

// AMMAccount computes the AMM pseudo-account for the given asset pair.
func AMMAccount(t *testing.T, asset1, asset2 tx.Asset) *jtx.Account {
	t.Helper()

	addr := coreAmm.ComputeAMMAccountAddress(asset1, asset2)
	_, idBytes, err := addresscodec.DecodeClassicAddressToAccountID(addr)
	if err != nil {
		t.Fatalf("Failed to decode AMM account address %s: %v", addr, err)
	}
	var id20 [20]byte
	copy(id20[:], idBytes)

	return &jtx.Account{
		Name:    "amm",
		Address: addr,
		ID:      id20,
	}
}

// AMMPoolXRP returns the XRP balance of the AMM pool in drops.
func (e *AMMTestEnv) AMMPoolXRP(ammAcc *jtx.Account) uint64 {
	e.T.Helper()
	return e.TestEnv.Balance(ammAcc)
}

// AMMPoolIOU returns the IOU balance of the AMM pool for the given currency.
func (e *AMMTestEnv) AMMPoolIOU(ammAcc *jtx.Account, issuer *jtx.Account, currency string) float64 {
	e.T.Helper()
	return e.TestEnv.BalanceIOU(ammAcc, currency, issuer)
}

// AMMPoolIOUPrecise returns the precise IOU balance of the AMM pool (full mantissa/exponent).
func (e *AMMTestEnv) AMMPoolIOUPrecise(ammAcc *jtx.Account, issuer *jtx.Account, currency string) tx.Amount {
	e.T.Helper()
	balance := e.TestEnv.IOUBalance(ammAcc, issuer, currency)
	if balance == nil {
		return tx.NewIssuedAmountFromFloat64(0, currency, issuer.Address)
	}
	return *balance
}

// accountFromAddress creates a jtx.Account with properly decoded ID from an address string.
func accountFromAddress(t *testing.T, addr string) *jtx.Account {
	t.Helper()
	_, idBytes, err := addresscodec.DecodeClassicAddressToAccountID(addr)
	if err != nil {
		t.Fatalf("accountFromAddress: failed to decode %s: %v", addr, err)
	}
	var id20 [20]byte
	copy(id20[:], idBytes)
	return &jtx.Account{Address: addr, ID: id20}
}

// AMMBalances returns (asset1Balance, asset2Balance, lptBalance) for the given AMM.
// This matches rippled's amm.balances(issue1, issue2) which returns the pool's IOU
// balances and total LP token supply.
func (e *AMMTestEnv) AMMBalances(asset1, asset2 tx.Asset) (tx.Amount, tx.Amount, tx.Amount) {
	e.T.Helper()

	ammAcc := AMMAccount(e.T, asset1, asset2)

	// Get pool IOU balances — must decode issuer addresses to get proper account IDs
	issuer1 := accountFromAddress(e.T, asset1.Issuer)
	issuer2 := accountFromAddress(e.T, asset2.Issuer)
	bal1 := e.AMMPoolIOUPrecise(ammAcc, issuer1, asset1.Currency)
	bal2 := e.AMMPoolIOUPrecise(ammAcc, issuer2, asset2.Currency)

	// Get LP token balance from AMM SLE
	ammData := e.ReadAMMData(asset1, asset2)
	if ammData == nil {
		// AMM deleted (e.g., after WithdrawAll) — all balances are zero
		zero := tx.NewIssuedAmountFromFloat64(0, "", "")
		return zero, zero, zero
	}
	lptBalance := ammData.LPTokenBalance

	return bal1, bal2, lptBalance
}

// CheckInvariant verifies the AMM invariant: sqrt(amount1 * amount2) >= lptBalance.
// When fixAMMv1_3 is enabled, uses upward rounding mode for the sqrt calculation.
// If shouldFail is true, expects the invariant to be violated (sqrt < lptBalance).
// Reference: rippled AMM_test.cpp invariant() function (line 7578)
func (e *AMMTestEnv) CheckInvariant(asset1, asset2 tx.Asset, fixAMMv1_3 bool, shouldFail bool, msg string) {
	e.T.Helper()

	bal1, bal2, lptBalance := e.AMMBalances(asset1, asset2)

	// Set rounding mode for the invariant check
	if fixAMMv1_3 {
		guard := sle.NewNumberRoundModeGuard(sle.RoundUpward)
		defer guard.Release()
	}

	// Compute sqrt(amount1 * amount2)
	product := bal1.Mul(bal2, false)
	result := product.Sqrt()

	cmp := result.Compare(lptBalance)
	if shouldFail {
		if cmp >= 0 {
			e.T.Errorf("invariant %s: expected violation (sqrt < lpt), but sqrt=%s >= lpt=%s",
				msg, result.Value(), lptBalance.Value())
		}
	} else {
		if cmp < 0 {
			e.T.Errorf("invariant %s: violated! sqrt=%s < lpt=%s (bal1=%s, bal2=%s)",
				msg, result.Value(), lptBalance.Value(), bal1.Value(), bal2.Value())
		}
	}
}

// ExpectAMMBalances checks that the AMM pool has the expected XRP and IOU balances.
// xrpDrops is in drops, iouAmount is as float64.
func (e *AMMTestEnv) ExpectAMMBalances(t *testing.T, ammAcc *jtx.Account, xrpDrops uint64, issuer *jtx.Account, currency string, iouAmount float64) {
	t.Helper()

	actualXRP := e.AMMPoolXRP(ammAcc)
	if actualXRP != xrpDrops {
		t.Errorf("AMM XRP balance: got %d drops, want %d drops", actualXRP, xrpDrops)
	}

	actualIOU := e.AMMPoolIOU(ammAcc, issuer, currency)
	// Allow small float tolerance
	diff := actualIOU - iouAmount
	if diff < 0 {
		diff = -diff
	}
	if diff > 0.0001 {
		t.Errorf("AMM %s balance: got %f, want %f", currency, actualIOU, iouAmount)
	}
}

// WithAMM sets up an AMM and calls the test callback.
// This matches rippled's testAMM helper function.
func WithAMM(t *testing.T, amount1, amount2 tx.Amount, tradingFee uint16, callback TestAMMCallback) {
	t.Helper()
	pool := [2]tx.Amount{amount1, amount2}
	TestAMM(t, &pool, tradingFee, callback)
}

// WithDefaultAMM sets up an AMM with XRP(10000)/USD(10000) and no trading fee.
func WithDefaultAMM(t *testing.T, callback TestAMMCallback) {
	t.Helper()
	TestAMM(t, nil, 0, callback)
}

// AccountOffers returns all offers owned by an account by iterating its owner directory.
// Returns a slice of parsed LedgerOffer structs.
func (e *AMMTestEnv) AccountOffers(acc *jtx.Account) []*sle.LedgerOffer {
	e.T.Helper()

	var offers []*sle.LedgerOffer
	dirKey := keylet.OwnerDir(acc.ID)
	_ = sle.DirForEach(e.Ledger(), dirKey, func(itemKey [32]byte) error {
		// Read the raw entry (Read doesn't check type)
		k := keylet.Keylet{Key: itemKey, Type: entry.TypeOffer}
		data, err := e.Ledger().Read(k)
		if err != nil || data == nil {
			return nil
		}
		// Check if the first bytes indicate an offer SLE.
		// Offer entries have LedgerEntryType = 0x006F.
		// The binary codec prefix starts with type/field codes; check for offer signature.
		offer, err := sle.ParseLedgerOfferFromBytes(data)
		if err != nil {
			return nil
		}
		// Only include if the offer has a valid account and non-zero amounts
		if offer.Account == "" || (offer.TakerPays.IsZero() && offer.TakerGets.IsZero()) {
			return nil // Not a valid offer entry
		}
		// Verify it belongs to the account we're querying
		if offer.Account != acc.Address {
			return nil
		}
		offers = append(offers, offer)
		return nil
	})

	return offers
}

// OfferCount returns the number of offers owned by an account.
func (e *AMMTestEnv) OfferCount(acc *jtx.Account) int {
	e.T.Helper()
	return len(e.AccountOffers(acc))
}

// NOffers creates n offers for the given account, closing the ledger after each.
// Reference: rippled's n_offers() in TestHelpers.cpp
func (e *AMMTestEnv) NOffers(n int, account *jtx.Account, takerPays, takerGets tx.Amount) {
	e.T.Helper()
	for i := 0; i < n; i++ {
		offerTx := offerbuild.OfferCreate(account, takerPays, takerGets).Build()
		result := e.Submit(offerTx)
		if !result.Success {
			e.T.Fatalf("NOffers: offer %d failed: %s - %s", i, result.Code, result.Message)
		}
		e.Close()
	}
}

// Vote sets the trading fee on an AMM pool.
// Reference: rippled's amm.vote(account, tradingFee)
func (e *AMMTestEnv) Vote(account *jtx.Account, asset1, asset2 tx.Asset, tradingFee uint16) {
	e.T.Helper()
	voteTx := AMMVote(account, asset1, asset2, tradingFee).Build()
	result := e.Submit(voteTx)
	if !result.Success {
		e.T.Fatalf("Vote failed: %s - %s", result.Code, result.Message)
	}
	e.Close()
}

// AMMTradingFee reads the current trading fee from the AMM SLE.
func (e *AMMTestEnv) AMMTradingFee(asset1, asset2 tx.Asset) uint16 {
	e.T.Helper()
	ammAddr := coreAmm.ComputeAMMAccountAddress(asset1, asset2)
	// Read via AMM keylet
	ammData := e.ReadAMMData(asset1, asset2)
	if ammData == nil {
		e.T.Fatalf("AMMTradingFee: AMM not found for %s/%s (addr=%s)", asset1.Currency, asset2.Currency, ammAddr)
	}
	return ammData.TradingFee
}

// ReadAMMData reads and parses the AMM SLE for the given asset pair.
func (e *AMMTestEnv) ReadAMMData(asset1, asset2 tx.Asset) *coreAmm.AMMData {
	e.T.Helper()
	// Build the keylet the same way the amm code does internally
	issuer1 := decodeIssuer(asset1.Issuer)
	currency1 := currencyToBytes(asset1.Currency)
	issuer2 := decodeIssuer(asset2.Issuer)
	currency2 := currencyToBytes(asset2.Currency)

	ammKey := keylet.AMM(issuer1, currency1, issuer2, currency2)
	data, err := e.Ledger().Read(ammKey)
	if err != nil || data == nil {
		return nil
	}
	ammData, err := coreAmm.ParseAMMData(data)
	if err != nil {
		e.T.Fatalf("ReadAMMData: parse error: %v", err)
	}
	return ammData
}

// decodeIssuer converts issuer address to [20]byte.
func decodeIssuer(issuer string) [20]byte {
	if issuer == "" {
		return [20]byte{}
	}
	_, bytes, err := addresscodec.DecodeClassicAddressToAccountID(issuer)
	if err != nil {
		return [20]byte{}
	}
	var id [20]byte
	copy(id[:], bytes)
	return id
}

// currencyToBytes converts a 3-letter currency code to [20]byte (ISO at bytes 12-14).
func currencyToBytes(currency string) [20]byte {
	if currency == "XRP" || currency == "" {
		return [20]byte{}
	}
	var b [20]byte
	copy(b[12:15], []byte(currency))
	return b
}

// AMMAssetOut computes the asset amount received for burning LP tokens.
// This is a test-exposed wrapper around the core ammAssetOut (Equation 8).
// Reference: rippled AMMHelpers.cpp ammAssetOut()
func AMMAssetOut(assetBalance, lptBalance, lpTokens tx.Amount, tfee uint16) tx.Amount {
	return coreAmm.AMMAssetOutExported(assetBalance, lptBalance, lpTokens, tfee)
}

// ExpectLPTokens checks that an account holds the expected amount of LP tokens.
// The LP token currency and issuer are derived from the asset pair.
func (e *AMMTestEnv) ExpectLPTokens(account *jtx.Account, asset1, asset2 tx.Asset, expected float64) {
	e.T.Helper()

	ammAcc := AMMAccount(e.T, asset1, asset2)
	lptCurrency := coreAmm.GenerateAMMLPTCurrency(asset1.Currency, asset2.Currency)

	balance := e.TestEnv.IOUBalance(account, ammAcc, lptCurrency)
	if balance == nil {
		if expected != 0 {
			e.T.Errorf("ExpectLPTokens(%s): no trust line, want %f", account.Name, expected)
		}
		return
	}
	actual := balance.Float64()

	// Allow small relative tolerance for rounding
	diff := actual - expected
	if diff < 0 {
		diff = -diff
	}
	tolerance := 0.01
	if expected != 0 {
		relTol := expected * 1e-10
		if relTol > tolerance {
			tolerance = relTol
		}
	}
	if diff > tolerance {
		e.T.Errorf("ExpectLPTokens(%s): got %f, want %f (diff=%f)", account.Name, actual, expected, diff)
	}
}

// ExpectLPTokensPrecise checks LP token balance with precise Amount comparison.
func (e *AMMTestEnv) ExpectLPTokensPrecise(account *jtx.Account, asset1, asset2 tx.Asset, expected tx.Amount) {
	e.T.Helper()

	ammAcc := AMMAccount(e.T, asset1, asset2)
	lptCurrency := coreAmm.GenerateAMMLPTCurrency(asset1.Currency, asset2.Currency)

	balance := e.TestEnv.IOUBalance(account, ammAcc, lptCurrency)
	if balance == nil {
		e.T.Errorf("ExpectLPTokensPrecise(%s): no trust line", account.Name)
		return
	}

	if balance.Mantissa() != expected.Mantissa() || balance.Exponent() != expected.Exponent() {
		e.T.Errorf("ExpectLPTokensPrecise(%s): got %de%d, want %de%d",
			account.Name, balance.Mantissa(), balance.Exponent(), expected.Mantissa(), expected.Exponent())
	}
}
