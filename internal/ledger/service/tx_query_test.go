package service

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestComputeBaseFeeForTx_Multisign verifies that the fee dispatch matches
// rippled Transactor::calculateBaseFee (Transactor.cpp:229-245):
// baseFee + signerCount * baseFee. The dispatch must not be gated on
// SigningPubKey being empty — rippled counts sfSigners entries directly.
func TestComputeBaseFeeForTx_Multisign(t *testing.T) {
	cfg := tx.EngineConfig{BaseFee: 10}

	t.Run("no signers → baseFee", func(t *testing.T) {
		parsed, err := tx.ParseJSON([]byte(`{
			"TransactionType":"AccountSet",
			"Account":"rEFNJWaJN6JYW9zXxFq1KqtaqgsMcLs9wK"
		}`))
		require.NoError(t, err)
		assert.Equal(t, uint64(10), computeBaseFeeForTx(nil, parsed, cfg))
	})

	t.Run("one signer with empty SigningPubKey → 2 * baseFee", func(t *testing.T) {
		parsed, err := tx.ParseJSON([]byte(`{
			"TransactionType":"AccountSet",
			"Account":"rEFNJWaJN6JYW9zXxFq1KqtaqgsMcLs9wK",
			"SigningPubKey":"",
			"Signers":[
				{"Signer":{"Account":"rPmsLuwgD3yp6mvCXyz44itC9V2qZpDvm6","SigningPubKey":"","TxnSignature":""}}
			]
		}`))
		require.NoError(t, err)
		assert.Equal(t, uint64(20), computeBaseFeeForTx(nil, parsed, cfg))
	})

	t.Run("one signer with non-empty SigningPubKey still gets multisign fee", func(t *testing.T) {
		// 33-byte compressed secp256k1 pubkey shape (66 hex chars). The value
		// itself is synthetic; the dispatch under test only counts sfSigners
		// (rippled Transactor.cpp:229-245), but a well-shaped pubkey keeps
		// this case robust against future parser tightening.
		const signingPubKey = "03ABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABAB"
		parsed, err := tx.ParseJSON([]byte(`{
			"TransactionType":"AccountSet",
			"Account":"rEFNJWaJN6JYW9zXxFq1KqtaqgsMcLs9wK",
			"SigningPubKey":"` + signingPubKey + `",
			"Signers":[
				{"Signer":{"Account":"rPmsLuwgD3yp6mvCXyz44itC9V2qZpDvm6","SigningPubKey":"","TxnSignature":""}}
			]
		}`))
		require.NoError(t, err)
		assert.Equal(t, uint64(20), computeBaseFeeForTx(nil, parsed, cfg),
			"rippled Transactor.cpp:229-245 counts sfSigners regardless of SigningPubKey")
	})

	t.Run("three signers → 4 * baseFee", func(t *testing.T) {
		parsed, err := tx.ParseJSON([]byte(`{
			"TransactionType":"AccountSet",
			"Account":"rEFNJWaJN6JYW9zXxFq1KqtaqgsMcLs9wK",
			"Signers":[
				{"Signer":{"Account":"rPmsLuwgD3yp6mvCXyz44itC9V2qZpDvm6","SigningPubKey":"","TxnSignature":""}},
				{"Signer":{"Account":"rN7n7otQDd6FczFgLdSqtcsAUxDkw6fzRH","SigningPubKey":"","TxnSignature":""}},
				{"Signer":{"Account":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh","SigningPubKey":"","TxnSignature":""}}
			]
		}`))
		require.NoError(t, err)
		assert.Equal(t, uint64(40), computeBaseFeeForTx(nil, parsed, cfg))
	})

	t.Run("nil parsedTx falls back to baseFee", func(t *testing.T) {
		assert.Equal(t, uint64(10), computeBaseFeeForTx(nil, nil, cfg))
	})
}

// TestComputeBaseFeeForTx_MaxMultiSigners verifies the rippled-faithful
// fallback to baseFee when the supplied Signers count exceeds
// STTx::maxMultiSigners (rippled TransactionSign.cpp:795-796 +
// STTx.h:55-63): 8 when ExpandedSignerList is supplied AND disabled,
// 32 otherwise (including when Rules is nil — rippled's permissive
// default).
func TestComputeBaseFeeForTx_MaxMultiSigners(t *testing.T) {
	rulesDisabled := amendment.NewRules(nil)
	rulesEnabled := amendment.NewRules([][32]byte{amendment.FeatureExpandedSignerList})

	t.Run("maxMultiSigners returns 8 when ExpandedSignerList is disabled", func(t *testing.T) {
		assert.Equal(t, 8, maxMultiSigners(rulesDisabled))
	})

	t.Run("maxMultiSigners returns 32 when Rules is nil", func(t *testing.T) {
		// rippled STTx.h:55-56: "if rules are not supplied then the
		// largest possible value is returned".
		assert.Equal(t, 32, maxMultiSigners(nil))
	})

	t.Run("maxMultiSigners returns 32 when ExpandedSignerList is enabled", func(t *testing.T) {
		assert.Equal(t, 32, maxMultiSigners(rulesEnabled))
	})

	t.Run("9 signers with ExpandedSignerList disabled falls back to baseFee", func(t *testing.T) {
		cfg := tx.EngineConfig{BaseFee: 10, Rules: rulesDisabled}

		parsed, err := tx.ParseJSON([]byte(buildSignersTxJSON(9)))
		require.NoError(t, err)
		assert.Equal(t, uint64(10), computeBaseFeeForTx(nil, parsed, cfg),
			"9 signers > maxMultiSigners(8) → reference_fee fallback per rippled TransactionSign.cpp:795")
	})

	t.Run("9 signers with nil Rules charges multisign fee", func(t *testing.T) {
		// Permissive default: nil Rules ⇒ cap=32, so 9 signers still
		// charge the full multisign fee (mirrors rippled STTx.h:55-56).
		cfg := tx.EngineConfig{BaseFee: 10, Rules: nil}

		parsed, err := tx.ParseJSON([]byte(buildSignersTxJSON(9)))
		require.NoError(t, err)
		assert.Equal(t, uint64(100), computeBaseFeeForTx(nil, parsed, cfg),
			"nil Rules ⇒ cap=32 ⇒ baseFee * (1 + 9) = 100")
	})

	t.Run("8 signers with ExpandedSignerList disabled charges multisign fee", func(t *testing.T) {
		cfg := tx.EngineConfig{BaseFee: 10, Rules: rulesDisabled}

		parsed, err := tx.ParseJSON([]byte(buildSignersTxJSON(8)))
		require.NoError(t, err)
		assert.Equal(t, uint64(90), computeBaseFeeForTx(nil, parsed, cfg),
			"baseFee * (1 + 8) = 90")
	})

	t.Run("9 signers with ExpandedSignerList enabled charges multisign fee", func(t *testing.T) {
		cfg := tx.EngineConfig{BaseFee: 10, Rules: rulesEnabled}

		parsed, err := tx.ParseJSON([]byte(buildSignersTxJSON(9)))
		require.NoError(t, err)
		assert.Equal(t, uint64(100), computeBaseFeeForTx(nil, parsed, cfg),
			"9 ≤ maxMultiSigners(32) → baseFee * (1 + 9) = 100")
	})
}

// buildSignersTxJSON returns a tx_json AccountSet with `count` synthetic
// signer entries. The signer accounts are not unique but ParseJSON does
// not enforce uniqueness — only structural shape matters for the
// computeBaseFeeForTx path under test.
func buildSignersTxJSON(count int) string {
	signerAccounts := []string{
		"rPmsLuwgD3yp6mvCXyz44itC9V2qZpDvm6",
		"rN7n7otQDd6FczFgLdSqtcsAUxDkw6fzRH",
		"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"rB5Ux4Lv2nRx6eeoAAsZmtctnBQ2LiACnk",
		"rNK7QSVHWvUuQzM7yJjJQbqGD1FpwjsiwL",
		"rUbPiEXkPHaPa3ECVAJBmYW6cTNkBzjJsB",
		"rGTQRcXdiBSCDuovpsFh5AssaiVbGM3JCG",
		"rwhMTPF8d6sLahMcjknKjHxBgrkRgaeoNS",
		"rDg53Haik2475DJx8bjMDSDPj4VX7htaMd",
	}
	entries := make([]string, 0, count)
	for i := range count {
		acct := signerAccounts[i%len(signerAccounts)]
		entries = append(entries, `{"Signer":{"Account":"`+acct+`","SigningPubKey":"","TxnSignature":""}}`)
	}
	return `{
		"TransactionType":"AccountSet",
		"Account":"rEFNJWaJN6JYW9zXxFq1KqtaqgsMcLs9wK",
		"Signers":[` + strings.Join(entries, ",") + `]
	}`
}

// panickingCustomFeeTx is a minimal tx.Transaction that implements
// CustomBaseFeeCalculator and panics inside CalculateBaseFee, used to
// verify computeBaseFeeForTx falls back to cfg.BaseFee on panic — the
// Go-side equivalent of rippled getTxFee's reference_fee fallback on
// any exception (TransactionSign.cpp:832-835).
type panickingCustomFeeTx struct{}

func (panickingCustomFeeTx) TxType() tx.Type                  { return tx.Type(0) }
func (panickingCustomFeeTx) GetCommon() *tx.Common            { return &tx.Common{} }
func (panickingCustomFeeTx) Validate() error                  { return nil }
func (panickingCustomFeeTx) Flatten() (map[string]any, error) { return nil, nil }
func (panickingCustomFeeTx) GetRawBytes() []byte              { return nil }
func (panickingCustomFeeTx) SetRawBytes([]byte)               {}
func (panickingCustomFeeTx) RequiredAmendments() [][32]byte   { return nil }
func (panickingCustomFeeTx) CalculateBaseFee(_ tx.LedgerView, _ tx.EngineConfig) uint64 {
	panic("simulated inconsistent view state")
}

func TestComputeBaseFeeForTx_CustomCalculatorPanicFallsBack(t *testing.T) {
	cfg := tx.EngineConfig{BaseFee: 42}
	got := computeBaseFeeForTx(nil, panickingCustomFeeTx{}, cfg)
	assert.Equal(t, uint64(42), got,
		"CustomBaseFeeCalculator panic must fall back to cfg.BaseFee — "+
			"mirrors rippled getTxFee reference_fee fallback (TransactionSign.cpp:832-835)")
}

func TestMulDivU64(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		got, ok := mulDivU64(10, 3, 2)
		assert.True(t, ok)
		assert.Equal(t, uint64(15), got)
	})
	t.Run("zero divisor", func(t *testing.T) {
		_, ok := mulDivU64(10, 3, 0)
		assert.False(t, ok)
	})
	t.Run("overflow", func(t *testing.T) {
		_, ok := mulDivU64(^uint64(0), 2, 1)
		assert.False(t, ok, "uint64 max * 2 overflows")
	})
}
