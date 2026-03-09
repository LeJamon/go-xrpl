package offer

import (
	"sort"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/stretchr/testify/require"
)

// rippleEpoch is the number of seconds between Unix epoch and Ripple epoch (Jan 1, 2000 00:00:00 UTC).
const rippleEpoch int64 = 946684800

// featureSet defines a set of disabled features for testing.
type featureSet struct {
	name     string
	disabled []string
}

// offerFeatureSets matches rippled's 6 feature combinations from Offer_test.cpp run() method.
// Reference: Offer_test.cpp lines 5365-5391
var offerFeatureSets = []featureSet{
	{
		name:     "base",
		disabled: []string{"fixTakerDryOfferRemoval", "ImmediateOfferKilled", "PermissionedDEX"},
	},
	{
		name:     "withTakerDry",
		disabled: []string{"ImmediateOfferKilled", "PermissionedDEX"},
	},
	{
		name:     "noSmallQ",
		disabled: []string{"fixRmSmallIncreasedQOffers", "ImmediateOfferKilled", "fixFillOrKill", "PermissionedDEX"},
	},
	{
		name:     "noFoK",
		disabled: []string{"fixFillOrKill", "PermissionedDEX"},
	},
	{
		name:     "noPermDEX",
		disabled: []string{"PermissionedDEX"},
	},
	{
		name:     "all",
		disabled: []string{},
	},
}

// newEnvWithFeatures creates a test environment with the given features disabled.
// Uses PebbleDB-backed SHAMaps to prevent OOM in heavy offer tests (crossing_limits etc.).
func newEnvWithFeatures(t *testing.T, disabledFeatures []string) *jtx.TestEnv {
	t.Helper()
	env := jtx.NewTestEnvBacked(t)
	for _, f := range disabledFeatures {
		env.DisableFeature(f)
	}
	return env
}

// featureEnabled checks if a feature is enabled (not in disabled list).
func featureEnabled(disabledFeatures []string, feature string) bool {
	for _, f := range disabledFeatures {
		if f == feature {
			return false
		}
	}
	return true
}

// Reserve computes the account reserve for a given owner count.
// Equivalent to rippled's reserve(env, count) helper in Offer_test.cpp.
func Reserve(env *jtx.TestEnv, count uint32) uint64 {
	return env.ReserveBase() + uint64(count)*env.ReserveIncrement()
}

// LastClose returns the parent close time in Ripple epoch seconds.
// Equivalent to rippled's lastClose(env) in Offer_test.cpp.
func LastClose(env *jtx.TestEnv) uint32 {
	unixSecs := env.Now().Unix()
	return uint32(unixSecs - rippleEpoch)
}

// RippleTimeFromUnix converts a time.Time to Ripple epoch seconds.
func RippleTimeFromUnix(t time.Time) uint32 {
	return uint32(t.Unix() - rippleEpoch)
}

// OfferInLedger checks if an offer exists in the ledger by account and sequence.
// Equivalent to rippled's offerInLedger / ledgerEntryOffer check.
func OfferInLedger(env *jtx.TestEnv, acc *jtx.Account, offerSeq uint32) bool {
	key := keylet.Offer(acc.ID, offerSeq)
	return env.LedgerEntryExists(key)
}

// GetOffer reads and parses a specific offer from the ledger by account and sequence.
// Returns nil if the offer doesn't exist.
func GetOffer(env *jtx.TestEnv, acc *jtx.Account, offerSeq uint32) *sle.LedgerOffer {
	key := keylet.Offer(acc.ID, offerSeq)
	data, err := env.LedgerEntry(key)
	if err != nil || len(data) == 0 {
		return nil
	}
	offer, err := sle.ParseLedgerOfferFromBytes(data)
	if err != nil {
		return nil
	}
	return offer
}

// CountOffers counts all offer entries owned by an account.
// Iterates the owner directory and filters for Offer type (0x006f).
// Equivalent to rippled's offers(account, N) require funclet.
func CountOffers(env *jtx.TestEnv, acc *jtx.Account) uint32 {
	dirKey := keylet.OwnerDir(acc.ID)
	var count uint32
	_ = sle.DirForEach(env.Ledger(), dirKey, func(itemKey [32]byte) error {
		entryKey := keylet.Keylet{Key: itemKey}
		data, readErr := env.LedgerEntry(entryKey)
		if readErr != nil || len(data) == 0 {
			return nil
		}
		entryType, typeErr := sle.GetLedgerEntryType(data)
		if typeErr != nil {
			return nil
		}
		// Offer type = 0x006f
		if entryType == 0x006f {
			count++
		}
		return nil
	})
	return count
}

// OffersOnAccount returns all parsed offer entries owned by an account.
func OffersOnAccount(env *jtx.TestEnv, acc *jtx.Account) []*sle.LedgerOffer {
	dirKey := keylet.OwnerDir(acc.ID)
	var offers []*sle.LedgerOffer
	_ = sle.DirForEach(env.Ledger(), dirKey, func(itemKey [32]byte) error {
		entryKey := keylet.Keylet{Key: itemKey}
		data, readErr := env.LedgerEntry(entryKey)
		if readErr != nil || len(data) == 0 {
			return nil
		}
		entryType, typeErr := sle.GetLedgerEntryType(data)
		if typeErr != nil {
			return nil
		}
		if entryType == 0x006f {
			offer, parseErr := sle.ParseLedgerOfferFromBytes(data)
			if parseErr == nil {
				offers = append(offers, offer)
			}
		}
		return nil
	})
	return offers
}

// SortedOffersOnAccount returns all offers for an account sorted by sequence number.
// Equivalent to rippled's sortedOffersOnAccount() in Offer_test.cpp.
func SortedOffersOnAccount(env *jtx.TestEnv, acc *jtx.Account) []*sle.LedgerOffer {
	offers := OffersOnAccount(env, acc)
	sort.Slice(offers, func(i, j int) bool {
		return offers[i].Sequence < offers[j].Sequence
	})
	return offers
}

// amountsEqual compares an sle.Amount from a parsed offer against a tx.Amount.
// Since tx.Amount is an alias for sle.Amount, we can use Compare directly.
func amountsEqual(a, b tx.Amount) bool {
	// First check both are same type (native vs issued)
	if a.IsNative() != b.IsNative() {
		return false
	}
	if a.IsNative() {
		return a.Drops() == b.Drops()
	}
	// For IOU: compare currency, issuer, and value
	if a.Currency != b.Currency || a.Issuer != b.Issuer {
		return false
	}
	return a.Compare(b) == 0
}

// computeRate returns the rate of an offer as a float64 (TakerPays / TakerGets).
// This is used for comparing offer quality degradation in reduced offer tests.
func computeRate(takerPays, takerGets tx.Amount) float64 {
	pays := takerPays.Float64()
	gets := takerGets.Float64()
	if gets == 0 {
		return 0
	}
	return pays / gets
}

// CountOffersMatching counts offers with specific TakerPays and TakerGets amounts.
// Equivalent to rippled's countOffers(env, account, takerPays, takerGets).
func CountOffersMatching(env *jtx.TestEnv, acc *jtx.Account, takerPays, takerGets tx.Amount) uint32 {
	var count uint32
	for _, offer := range OffersOnAccount(env, acc) {
		if amountsEqual(offer.TakerPays, takerPays) && amountsEqual(offer.TakerGets, takerGets) {
			count++
		}
	}
	return count
}

// IsOffer checks if an offer with specific amounts exists for an account.
// Equivalent to rippled's isOffer(env, account, takerPays, takerGets).
func IsOffer(env *jtx.TestEnv, acc *jtx.Account, takerPays, takerGets tx.Amount) bool {
	return CountOffersMatching(env, acc, takerPays, takerGets) > 0
}

// RequireOfferCount asserts that an account has exactly the expected number of offers.
func RequireOfferCount(t *testing.T, env *jtx.TestEnv, acc *jtx.Account, expected uint32) {
	t.Helper()
	actual := CountOffers(env, acc)
	require.Equal(t, expected, actual,
		"Account %s offer count mismatch: expected %d, got %d",
		acc.Name, expected, actual)
}

// RequireIsOffer asserts that an offer with specific amounts exists.
func RequireIsOffer(t *testing.T, env *jtx.TestEnv, acc *jtx.Account, takerPays, takerGets tx.Amount) {
	t.Helper()
	require.True(t, IsOffer(env, acc, takerPays, takerGets),
		"Expected offer (TakerPays=%v, TakerGets=%v) on account %s to exist",
		takerPays, takerGets, acc.Name)
}

// RequireNoOffer asserts that an offer with specific amounts does not exist.
func RequireNoOffer(t *testing.T, env *jtx.TestEnv, acc *jtx.Account, takerPays, takerGets tx.Amount) {
	t.Helper()
	require.False(t, IsOffer(env, acc, takerPays, takerGets),
		"Expected offer (TakerPays=%v, TakerGets=%v) on account %s to NOT exist",
		takerPays, takerGets, acc.Name)
}

// RequireOfferInLedger asserts that an offer entry exists in the ledger by sequence.
func RequireOfferInLedger(t *testing.T, env *jtx.TestEnv, acc *jtx.Account, offerSeq uint32) {
	t.Helper()
	require.True(t, OfferInLedger(env, acc, offerSeq),
		"Expected offer (account=%s, seq=%d) to exist in ledger",
		acc.Name, offerSeq)
}

// RequireNoOfferInLedger asserts that an offer entry does NOT exist in the ledger by sequence.
func RequireNoOfferInLedger(t *testing.T, env *jtx.TestEnv, acc *jtx.Account, offerSeq uint32) {
	t.Helper()
	require.False(t, OfferInLedger(env, acc, offerSeq),
		"Expected offer (account=%s, seq=%d) to NOT exist in ledger",
		acc.Name, offerSeq)
}
