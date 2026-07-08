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
	// These EscrowToken cases delete the escrow's referenced MPT issuance via
	// open-ledger surgery (env.app().openLedger().modify() in EscrowToken_test.cpp
	// testMPTFinishPreclaim/testMPTCancelPreclaim), without a transaction, so the
	// EscrowFinish/Cancel then sees a missing issuance and rippled returns
	// tecOBJECT_NOT_FOUND. The fixture cannot represent that transaction-less
	// deletion, so the issuance still exists and go-xrpl returns tecNO_TARGET. The
	// MPT-escrow preclaim logic is exercised by the escrow unit tests.
	"app/EscrowToken/MPT_Finish_Preclaim": "referenced MPT issuance deleted via open-ledger surgery not representable in fixtures",
	"app/EscrowToken/MPT_Cancel_Preclaim": "referenced MPT issuance deleted via open-ledger surgery not representable in fixtures",
	// rippled grows the TxQ's open-ledger capacity (txnsExpected) via the
	// amendment-vote pseudo-transactions injected at flag ledgers over these 513
	// closes; the fixture records those closes as empty, so go-xrpl never sees them
	// and stays at the harness-minimum capacity, queuing sooner. The admission and
	// escalation logic itself is exercised by the other TxQMetaInfo and
	// TxQPosNegFlows fixtures.
	"app/TxQMetaInfo/Re-execute_preflight": "open-ledger capacity depends on rippled-internal amendment-vote consensus activity not captured in the fixture's empty closes",
	// These Vault fixtures were recorded before rippled #5954: they assert the
	// pre-#5954 owner-count model (VaultCreate owner_count +1, no owner
	// share-MPToken at create), whereas 3.1.x VaultCreate charges the owner for
	// the vault + pseudo-account and creates the owner's share MPToken
	// (owner_count +3). The transaction results (TER) match; only the recorded
	// owner_count / coupled balances are stale. Re-record from rippled 3.1.x.
	"app/Vault/transaction_is_good":                            "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/private_vault":                                  "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/private_XRP_vault":                              "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/private_XRP_vault_owner_can_deposit":            "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/private_XRP_vault_depositor_not_authorized_yet": "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/private_XRP_vault_depositor_now_authorized":     "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/private_XRP_vault_set_DomainID":                 "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/private_vault_owner_can_deposit":                "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/private_vault_depositor_not_authorized_yet":     "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/private_vault_set_domainId":                     "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/private_vault_cannot_set_non-existing_domain":   "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/explicitly_select_withdrawal_policy":            "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/nontransferable_deposits":                       "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/nontransferable_shares_can_be_used_to_withdraw": "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/RPC":                                                 "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/IOU_set_data":                                        "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/IOU_deposit_non-zero_amount":                         "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/IOU_deposit_non-zero_amount_again":                   "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/IOU_deposit_again":                                   "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/IOU_deposit_some_more":                               "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/IOU_clawback_some":                                   "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/IOU_no_trust_line_to_depositor":                      "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/IOU_reset_maximum_to_zero_i.e._not_enforced":         "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/IOU_set_maximum_higher_than_current_amount":          "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/IOU_fail_to_delete_non-empty_vault":                  "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/IOU_fail_to_deposit_more_than_assets_held":           "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/IOU_fail_to_deposit_more_than_maximum":               "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/IOU_fail_to_set_domain_on_public_vault":              "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/IOU_fail_to_set_maximum_lower_than_current_amount":   "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/IOU_fail_to_update_because_wrong_owner":              "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/IOU_fail_to_withdraw_more_than_assets_held":          "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/IOU_fail_to_withdraw_to_3rd_party_lsfDepositAuth":    "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/IOU_fail_to_withdraw_to_3rd_party_no_authorization":  "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/MPT_set_data":                                        "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/MPT_deposit_non-zero_amount":                         "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/MPT_deposit_non-zero_amount_again":                   "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/MPT_deposit_again":                                   "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/MPT_deposit_some_more":                               "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/MPT_withdraw_remaining_assets":                       "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/MPT_withdraw_to_authorized_3rd_party":                "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/MPT_withdraw_to_issuer":                              "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/MPT_reset_maximum_to_zero_i.e._not_enforced":         "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/MPT_set_maximum_higher_than_current_amount":          "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/MPT_fail_to_delete_because_wrong_owner":              "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/MPT_fail_to_delete_non-empty_vault":                  "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/MPT_fail_to_deposit_more_than_assets_held":           "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/MPT_fail_to_deposit_more_than_maximum":               "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/MPT_fail_to_set_domain_on_public_vault":              "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/MPT_fail_to_set_maximum_lower_than_current_amount":   "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/MPT_fail_to_update_because_wrong_owner":              "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/MPT_fail_to_withdraw_more_than_assets_held":          "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/MPT_fail_to_withdraw_to_3rd_party_lsfDepositAuth":    "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/MPT_fail_to_withdraw_to_3rd_party_lsfRequireDestTag": "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	"app/Vault/MPT_fail_to_withdraw_to_3rd_party_no_authorization":  "fixture predates rippled #5954 owner-count model; re-record from rippled 3.1.x",
	// These Delegate fixtures were recorded under the deleted PermissionDelegation
	// / fixDelegateV1_1 amendments. rippled 3.2.0 replaced them with
	// PermissionDelegationV1_1, which folds the fix behaviours in unconditionally
	// and changes the delegate-permission denial code from tecNO_DELEGATE_PERMISSION
	// (removed) to the retriable terNO_DELEGATE_PERMISSION, and the non-delegatable
	// rejection from tecNO_PERMISSION (preclaim) to temMALFORMED (preflight). The
	// fixtures encode the pre-V1_1 results and cannot be satisfied; the V1_1
	// behaviour is covered by internal/testing/delegate. Re-record from rippled 3.2.0.
	"app/Delegate/test_delegate_transaction":                                   "fixture predates PermissionDelegationV1_1; re-record from rippled 3.2.0",
	"app/Delegate/test_invalid_DelegateSet":                                    "fixture predates PermissionDelegationV1_1; re-record from rippled 3.2.0",
	"app/Delegate/test_valid_request_creating,_updating,_deleting_permissions": "fixture predates PermissionDelegationV1_1; re-record from rippled 3.2.0",
	"app/Delegate/test_payment_granular":                                       "fixture predates PermissionDelegationV1_1; re-record from rippled 3.2.0",
	"app/Delegate/test_AccountSet_granular_permissions":                        "fixture predates PermissionDelegationV1_1; re-record from rippled 3.2.0",
	"app/Delegate/test_TrustSet_granular_permissions":                          "fixture predates PermissionDelegationV1_1; re-record from rippled 3.2.0",
	"app/Delegate/test_MPTokenIssuanceSet_granular":                            "fixture predates PermissionDelegationV1_1; re-record from rippled 3.2.0",
	"app/Delegate/test_single_sign":                                            "fixture predates PermissionDelegationV1_1; re-record from rippled 3.2.0",
	"app/Delegate/test_single_sign_with_bad_secret":                            "fixture predates PermissionDelegationV1_1; re-record from rippled 3.2.0",
	"app/Delegate/test_multi_sign":                                             "fixture predates PermissionDelegationV1_1; re-record from rippled 3.2.0",
	"app/Delegate/test_multi_sign_which_does_not_meet_quorum":                  "fixture predates PermissionDelegationV1_1; re-record from rippled 3.2.0",
	"app/Delegate/test_reserve":                                                "fixture predates PermissionDelegationV1_1; re-record from rippled 3.2.0",
	"app/Delegate/test_fee":                                                    "fixture predates PermissionDelegationV1_1; re-record from rippled 3.2.0",
	"app/Delegate/test_sequence":                                               "fixture predates PermissionDelegationV1_1; re-record from rippled 3.2.0",
	"app/Delegate/test_deleting_account":                                       "fixture predates PermissionDelegationV1_1; re-record from rippled 3.2.0",
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
