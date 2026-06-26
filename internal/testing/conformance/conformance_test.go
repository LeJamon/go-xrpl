package conformance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixturesRoot is the path to the xrpl-fixtures directory relative to this test file.
// Adjust this if the fixtures are located elsewhere.
const fixturesRoot = "../../../../fixtures/rippled-2.6.2-v2"

// skipTests lists individual test names (relative path without .json) that are
// structurally incompatible with the conformance runner and should be skipped.
// These are NOT implementation gaps — they test behaviors that depend on
// rippled-internal state (parentHash, ledger sequence hashing) that differs
// between rippled and go-xrpl by design.
var skipTests = map[string]string{
	// Pseudo-account collision tests create accounts at addresses derived from
	// sha512Half(i, parentHash, ammKeylet). Since go-xrpl has a different
	// parentHash than rippled, the collision addresses don't match and the
	// test cannot work. The underlying AMMCreate collision detection is
	// tested via unit tests instead.
	"app/AMM/Failed_pseudo-account_allocation_tecDUPLICATE":         "parentHash-dependent pseudo-account collision",
	"app/AMM/Failed_pseudo-account_allocation_terADDRESS_COLLISION": "parentHash-dependent pseudo-account collision",
	// C++ STObject template caching bug: when fixInnerObjTemplate is disabled and
	// TradingFee=0, rippled's inner object deserialization throws tefEXCEPTION due
	// to soeDEFAULT field validation in STObject::applyTemplate(). Go uses plain
	// structs with no template system, so this bug cannot occur. No Go equivalent
	// exists to emulate.
	"app/AMM/Fix_Default_Inner_Object": "C++ STObject template caching bug, no Go equivalent",
	// These NFTokenAuth cases rely on rippled injecting a transient *unauthorized*
	// trustline that carries a balance directly into the open ledger
	// (env.app().openLedger().modify() + rawInsert in NFTokenAuth_test.cpp),
	// explicitly without closing the ledger so the line never persists. The
	// fixture format records only transactions and closed post-state, so this
	// transaction-less injected state cannot be reproduced. Without an
	// unauthorized-but-funded line the funds check legitimately fires first, so
	// go-xrpl returns tecUNFUNDED_OFFER / tecINSUFFICIENT_FUNDS instead of
	// tecNO_AUTH. The tecNO_AUTH authorization logic itself is correct and is
	// exercised directly by internal/testing/nft/nftoken_auth_test.go.
	"app/NFTokenAuth/Unauthorized_buyer_tries_to_create_buy_offer":                      "unauthorized-trustline open-ledger injection not representable in fixtures",
	"app/NFTokenAuth/Seller_tries_to_accept_buy_offer_from_unauth_buyer":                "unauthorized-trustline open-ledger injection not representable in fixtures",
	"app/NFTokenAuth/Unauthorized_buyer_tries_to_accept_sell_offer":                     "unauthorized-trustline open-ledger injection not representable in fixtures",
	"app/NFTokenAuth/Authorized_broker_tries_to_bridge_offers_from_unauthorized_buyer.": "unauthorized-trustline open-ledger injection not representable in fixtures",
}

func TestConformance(t *testing.T) {
	root, err := filepath.Abs(fixturesRoot)
	if err != nil {
		t.Fatalf("Failed to resolve fixtures root: %v", err)
	}

	if _, err := os.Stat(root); os.IsNotExist(err) {
		t.Skipf("Fixtures directory not found at %s — skipping conformance tests", root)
	}

	// Walk the fixtures directory and create a subtest per fixture file
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}

		// Build test name from relative path: "app/Escrow/Lockup"
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		testName := strings.TrimSuffix(rel, ".json")

		fixturePath := path
		t.Run(testName, func(t *testing.T) {
			// Skip structurally incompatible tests
			if reason, skip := skipTests[testName]; skip {
				t.Skipf("Skipped: %s", reason)
				return
			}
			t.Parallel()
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("PANIC: %v", fmt.Sprintf("%v", r))
				}
			}()
			RunFixture(t, fixturePath)
		})

		return nil
	})
	if err != nil {
		t.Fatalf("Failed to walk fixtures directory: %v", err)
	}
}
