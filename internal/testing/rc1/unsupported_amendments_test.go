// Package rc1_test covers the rippled 3.5.0-rc1 protocol surface this node can
// decode while its amendments stay unsupported.
package rc1_test

import (
	"encoding/hex"
	"maps"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

const (
	issuanceID = "00000001A407AF5856CCF3C42619DAA925813FC955C72983"
	hash256    = "8F2E5B4C1D3A6E7F9081726354A5B6C7D8E9F0A1B2C3D4E5F60718293A4B5C6D"
)

// submitBinary round-trips fields through the binary codec, as a peer-relayed
// or submit-blob transaction would arrive, then applies the result.
func submitBinary(t *testing.T, env *jtx.TestEnv, account *jtx.Account, fields map[string]any) jtx.TxResult {
	t.Helper()
	fields["Account"] = account.Address
	fields["Fee"] = "10"
	fields["Sequence"] = env.Seq(account)
	fields["SigningPubKey"] = ""
	encoded, err := binarycodec.Encode(fields)
	require.NoError(t, err)
	blob, err := hex.DecodeString(encoded)
	require.NoError(t, err)
	parsed, err := tx.ParseFromBinary(blob)
	require.NoError(t, err)
	return env.Submit(parsed)
}

// rippled rejects a transaction type whose amendment is disabled with
// temDISABLED before any other preflight check.
func TestRC1TransactionTypesDisabled(t *testing.T) {
	for name, fields := range map[string]map[string]any{
		"ConfidentialMPTMirrorUpdate": {
			"TransactionType":   "ConfidentialMPTMirrorUpdate",
			"MPTokenIssuanceID": issuanceID,
			"ZKProof":           "AB",
		},
		"ConfidentialMPTHolderKeyUpdate": {
			"TransactionType":   "ConfidentialMPTHolderKeyUpdate",
			"MPTokenIssuanceID": issuanceID,
		},
		"TransactionProposalCreate": {
			"TransactionType": "TransactionProposalCreate",
			"Expiration":      uint32(1_000_000),
			"ProposedTransaction": map[string]any{
				"TransactionType": "AccountSet",
				"Account":         "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
				"Sequence":        uint32(1),
				"Fee":             "10",
			},
		},
		"TransactionProposalCancel": {
			"TransactionType": "TransactionProposalCancel",
			"ProposalID":      hash256,
		},
	} {
		t.Run(name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			alice := jtx.NewAccount("alice")
			env.Fund(alice)
			env.Close()

			result := submitBinary(t, env, alice, fields)
			require.Equal(t, "temDISABLED", result.Code, result.Message)
		})
	}
}

// New optional fields on existing transactions are rejected while their
// amendment is disabled (T::checkExtraFeatures); the same transaction without
// the field gets past that gate.
func TestRC1OptionalFieldsDisabled(t *testing.T) {
	for _, tc := range []struct {
		field  string
		value  any
		fields map[string]any
	}{
		{"Bytecode", "0061736D", map[string]any{
			"TransactionType": "EscrowCreate",
			"Destination":     "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
			"Amount":          "1000000",
			"FinishAfter":     uint32(1_000_000_000),
		}},
		{"Data", "01", map[string]any{
			"TransactionType": "EscrowCreate",
			"Destination":     "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
			"Amount":          "1000000",
			"FinishAfter":     uint32(1_000_000_000),
		}},
		{"Gas", uint32(100), map[string]any{
			"TransactionType": "EscrowFinish",
			"Owner":           "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
			"OfferSequence":   uint32(1),
		}},
		{"DomainID", hash256, map[string]any{
			"TransactionType": "LoanBrokerSet",
			"VaultID":         hash256,
		}},
	} {
		t.Run(tc.fields["TransactionType"].(string)+"/"+tc.field, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			alice := jtx.NewAccount("alice")
			env.Fund(alice)
			env.Close()

			without := submitBinary(t, env, alice, maps.Clone(tc.fields))
			require.NotEqual(t, "temDISABLED", without.Code, without.Message)

			with := maps.Clone(tc.fields)
			with[tc.field] = tc.value
			result := submitBinary(t, env, alice, with)
			require.Equal(t, "temDISABLED", result.Code, result.Message)
		})
	}
}
