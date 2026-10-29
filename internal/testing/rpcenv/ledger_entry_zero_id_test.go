package rpcenv

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// rippled parses hash IDs with base_uint::parseHex, which reads "0" as the
// zero ID; no ledger entry lives there, so every hex selector answers
// entryNotFound rather than a parse error.
func TestLedgerEntryZeroHexIDs(t *testing.T) {
	env := New(t)
	const genesis = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	hexSelectors := []string{
		"index", "amendments", "fee", "nunl", "hashes", "amm", "check", "credential",
		"delegate", "deposit_preauth", "directory", "escrow", "loan", "loan_broker", "mpt_issuance",
		"mptoken", "nft_offer", "nft_page", "offer", "oracle", "payment_channel",
		"permissioned_domain", "ripple_state", "state", "signer_list", "ticket", "vault",
		"xchain_owned_claim_id", "xchain_owned_create_account_claim_id",
	}
	cases := make(map[string]map[string]any, len(hexSelectors)+3)
	for _, selector := range hexSelectors {
		cases[selector] = map[string]any{selector: "0"}
	}
	cases["directory dir_root"] = map[string]any{"directory": map[string]any{"dir_root": "0"}}
	cases["mptoken issuance"] = map[string]any{"mptoken": map[string]any{"mpt_issuance_id": "0", "account": genesis}}
	cases["loan broker id"] = map[string]any{"loan": map[string]any{"loan_broker_id": "0", "loan_seq": 1}}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			_, rpcErr := env.RPC("ledger_entry", params)
			require.NotNil(t, rpcErr)
			require.Equal(t, "entryNotFound", rpcErr.ErrorString)
		})
	}

	// A string bridge is never read as an ID: rippled hands the whole request
	// to the object-ID parser, which rejects it.
	_, rpcErr := env.RPC("ledger_entry", map[string]any{"bridge": "0"})
	require.NotNil(t, rpcErr)
	require.Equal(t, "malformedRequest", rpcErr.ErrorString)
}
