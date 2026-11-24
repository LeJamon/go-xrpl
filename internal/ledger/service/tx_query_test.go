package service

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/internal/tx/escrow"
	"github.com/LeJamon/go-xrpl/internal/tx/ledgerstatefix"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCustomBaseFeesUseParentSchedule(t *testing.T) {
	fees := drops.Fees{Base: 17, Reserve: 34_000_000, Increment: 5_000_000}
	view, err := ledger.NewOpenWithHeader(header.LedgerHeader{}, shamap.New(shamap.TypeState), shamap.New(shamap.TypeTransaction), fees)
	require.NoError(t, err)
	data, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType": "FeeSettings", "Flags": uint32(0), "BaseFeeDrops": "23",
	})
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.Fees(), data))
	config := tx.EngineConfig{BaseFee: uint64(fees.Base), ReserveIncrement: uint64(fees.Increment)}

	for _, tc := range []struct {
		txn  tx.Transaction
		want uint64
	}{
		{account.NewAccountDelete("alice", "bob"), 5_000_000},
		{ledgerstatefix.NewNFTokenPageLinkFix("alice", "bob"), 5_000_000},
		{escrow.NewEscrowFinish("alice", "bob", 1), 17},
	} {
		got, err := sign.CalculateBaseFee(tc.txn, view, config)
		require.NoError(t, err)
		require.Equal(t, tc.want, got, "%T", tc.txn)
	}
}

// TestEstimateSigningBaseFee_Multisign verifies that the fee dispatch matches
// rippled Transactor::calculateBaseFee (Transactor.cpp:229-245):
// baseFee + signerCount * baseFee. The dispatch must not be gated on
// SigningPubKey being empty — rippled counts sfSigners entries directly.
func TestEstimateSigningBaseFee_Multisign(t *testing.T) {
	cfg := tx.EngineConfig{BaseFee: 10}

	t.Run("no signers → baseFee", func(t *testing.T) {
		parsed, err := tx.ParseJSON([]byte(`{
			"TransactionType":"AccountSet",
			"Account":"rEFNJWaJN6JYW9zXxFq1KqtaqgsMcLs9wK"
		}`))
		require.NoError(t, err)
		assert.Equal(t, uint64(10), estimateSigningBaseFee(nil, parsed, cfg, cfg.BaseFee))
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
		assert.Equal(t, uint64(20), estimateSigningBaseFee(nil, parsed, cfg, cfg.BaseFee))
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
		assert.Equal(t, uint64(20), estimateSigningBaseFee(nil, parsed, cfg, cfg.BaseFee),
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
		assert.Equal(t, uint64(40), estimateSigningBaseFee(nil, parsed, cfg, cfg.BaseFee))
	})

	t.Run("nil parsedTx falls back to baseFee", func(t *testing.T) {
		assert.Equal(t, uint64(10), estimateSigningBaseFee(nil, nil, cfg, cfg.BaseFee))
	})
}

// TestEstimateSigningBaseFee_MaxMultiSigners verifies the rippled-faithful
// fallback to baseFee when the supplied Signers count exceeds the multi-signer
// cap (STTx::kMaxMultiSigners = 32, unconditional since ExpandedSignerList
// retired).
func TestEstimateSigningBaseFee_MaxMultiSigners(t *testing.T) {
	t.Run("9 signers charges multisign fee", func(t *testing.T) {
		cfg := tx.EngineConfig{BaseFee: 10}

		parsed, err := tx.ParseJSON([]byte(buildSignersTxJSON(9)))
		require.NoError(t, err)
		assert.Equal(t, uint64(100), estimateSigningBaseFee(nil, parsed, cfg, cfg.BaseFee),
			"9 ≤ 32 ⇒ baseFee * (1 + 9) = 100")
	})

	t.Run("32 signers charges multisign fee", func(t *testing.T) {
		cfg := tx.EngineConfig{BaseFee: 10}

		parsed, err := tx.ParseJSON([]byte(buildSignersTxJSON(32)))
		require.NoError(t, err)
		assert.Equal(t, uint64(330), estimateSigningBaseFee(nil, parsed, cfg, cfg.BaseFee),
			"32 ≤ 32 ⇒ baseFee * (1 + 32) = 330")
	})

	t.Run("33 signers falls back to baseFee", func(t *testing.T) {
		cfg := tx.EngineConfig{BaseFee: 10}

		parsed, err := tx.ParseJSON([]byte(buildSignersTxJSON(33)))
		require.NoError(t, err)
		assert.Equal(t, uint64(10), estimateSigningBaseFee(nil, parsed, cfg, cfg.BaseFee),
			"33 > 32 → reference_fee fallback")
	})
}

// buildSignersTxJSON returns a tx_json AccountSet with `count` synthetic
// signer entries. The signer accounts are not unique but ParseJSON does
// not enforce uniqueness — only structural shape matters for the
// estimateSigningBaseFee path under test.
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
// verify estimateSigningBaseFee falls back to cfg.BaseFee on panic — the
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
func (panickingCustomFeeTx) CalculateBaseFee(_ tx.ReadOnlyLedgerView, _ tx.EngineConfig) (uint64, error) {
	panic("simulated inconsistent view state")
}

func TestEstimateSigningBaseFee_CustomCalculatorPanicFallsBack(t *testing.T) {
	cfg := tx.EngineConfig{BaseFee: 42}
	got := estimateSigningBaseFee(nil, panickingCustomFeeTx{}, cfg, cfg.BaseFee)
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
