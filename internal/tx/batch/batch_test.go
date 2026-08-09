package batch

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/clawback"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func TestMain(m *testing.M) {
	Register()
	payment.Register()
	os.Exit(m.Run())
}

func TestBatchBinaryRoundTripPreservesInnerTransactions(t *testing.T) {
	outer := NewBatch(testOuter)
	outer.Fee = "40"
	outerSequence := uint32(1)
	outer.Sequence = &outerSequence
	flags := BatchFlagAllOrNothing
	outer.Flags = &flags
	outer.AddInnerTransaction(makeTestPayment())
	outer.AddInnerTransaction(makeTestPayment())
	outer.BatchSigners = []BatchSigner{{BatchSigner: BatchSignerData{
		Account:           testSigner1,
		SigningPubKey:     "ED0000000000000000000000000000000000000000000000000000000000000000",
		BatchTxnSignature: "ABCD",
	}}}

	flat, err := outer.Flatten()
	require.NoError(t, err)
	require.Equal(t, "", flat["SigningPubKey"])
	encoded, err := binarycodec.Encode(flat)
	require.NoError(t, err)
	blob, err := hex.DecodeString(encoded)
	require.NoError(t, err)

	parsed, err := tx.ParseFromBinary(blob)
	require.NoError(t, err)
	parsedBatch, ok := parsed.(*Batch)
	require.True(t, ok)
	require.Len(t, parsedBatch.RawTransactions, 2)
	require.Len(t, parsedBatch.BatchSigners, 1)
	require.Equal(t, "ABCD", parsedBatch.BatchSigners[0].BatchSigner.BatchTxnSignature)

	for i, rawTransaction := range parsedBatch.RawTransactions {
		inner := rawTransaction.RawTransaction.InnerTx
		require.NotNil(t, inner)
		require.NotEmpty(t, inner.GetRawBytes())
		decoded, err := binarycodec.DecodeBytes(inner.GetRawBytes())
		require.NoError(t, err)
		value, present := decoded["SigningPubKey"]
		require.True(t, present)
		require.Equal(t, "", value)

		original := outer.RawTransactions[i].RawTransaction.InnerTx
		originalHash, err := tx.ComputeTransactionHash(original)
		require.NoError(t, err)
		parsedHash, err := tx.ComputeTransactionHash(inner)
		require.NoError(t, err)
		require.Equal(t, originalHash, parsedHash)
	}
}

func TestBatchBinaryParseRejectsStructuralAbuseBeforeInnerConstruction(t *testing.T) {
	outer := NewBatch(testOuter)
	outer.Fee = "40"
	outerSequence := uint32(1)
	outer.Sequence = &outerSequence
	flags := BatchFlagAllOrNothing
	outer.Flags = &flags
	for range MaxBatchTransactions + 1 {
		outer.AddInnerTransaction(makeTestPayment())
	}

	flat, err := outer.Flatten()
	require.NoError(t, err)
	encoded, err := binarycodec.Encode(flat)
	require.NoError(t, err)
	blob, err := hex.DecodeString(encoded)
	require.NoError(t, err)
	_, err = tx.ParseFromBinary(blob)
	require.ErrorContains(t, err, "Raw Transactions array exceeds max entries")
	jsonBlob, err := json.Marshal(flat)
	require.NoError(t, err)
	_, err = tx.ParseJSON(jsonBlob)
	require.ErrorContains(t, err, "Raw Transactions array exceeds max entries")

	nested := NewBatch(testOuter)
	nested.Fee = "0"
	nested.Sequence = &outerSequence
	nested.Flags = &flags
	nested.AddInnerTransaction(makeTestPayment())
	nested.AddInnerTransaction(makeTestPayment())
	outer = NewBatch(testOuter)
	outer.Fee = "40"
	outer.Sequence = &outerSequence
	outer.Flags = &flags
	outer.AddInnerTransaction(nested)
	outer.AddInnerTransaction(makeTestPayment())
	flat, err = outer.Flatten()
	require.NoError(t, err)
	encoded, err = binarycodec.Encode(flat)
	require.NoError(t, err)
	blob, err = hex.DecodeString(encoded)
	require.NoError(t, err)
	_, err = tx.ParseFromBinary(blob)
	require.ErrorContains(t, err, "Raw Transactions may not contain batch transactions")
	jsonBlob, err = json.Marshal(flat)
	require.NoError(t, err)
	_, err = tx.ParseJSON(jsonBlob)
	require.ErrorContains(t, err, "Raw Transactions may not contain batch transactions")
}

func TestBatchLocalChecksRecurseIntoBinaryInners(t *testing.T) {
	outer := NewBatch(testOuter)
	outer.Fee = "40"
	outerSequence := uint32(1)
	outer.Sequence = &outerSequence
	flags := BatchFlagAllOrNothing
	outer.Flags = &flags
	inner := makeTestPayment()
	inner.GetCommon().Memos = []tx.MemoWrapper{{Memo: tx.Memo{MemoData: strings.Repeat("AA", 1100)}}}
	outer.AddInnerTransaction(inner)
	outer.AddInnerTransaction(makeTestPayment())

	flat, err := outer.Flatten()
	require.NoError(t, err)
	encoded, err := binarycodec.Encode(flat)
	require.NoError(t, err)
	blob, err := hex.DecodeString(encoded)
	require.NoError(t, err)
	parsed, err := tx.ParseFromBinary(blob)
	require.NoError(t, err)
	require.Equal(t, ter.TemMALFORMED, tx.PassesTransactionLocalChecks(parsed))
	require.Contains(t, tx.TransactionLocalChecksFailureReason(parsed), "memo exceeds")
}

func TestBatchBinaryRoundTripPreservesTicketedInnerSequence(t *testing.T) {
	outer := NewBatch(testOuter)
	outer.Fee = "40"
	outerSequence := uint32(1)
	outer.Sequence = &outerSequence
	flags := BatchFlagAllOrNothing
	outer.Flags = &flags

	ticketed := makeTestPayment()
	ticketSequence := uint32(7)
	ticketed.GetCommon().Sequence = nil
	ticketed.GetCommon().TicketSequence = &ticketSequence
	outer.AddInnerTransaction(ticketed)
	outer.AddInnerTransaction(makeTestPayment())

	flat, err := outer.Flatten()
	require.NoError(t, err)
	rawTransactions := flat["RawTransactions"].([]map[string]any)
	ticketedMap := rawTransactions[0]["RawTransaction"].(map[string]any)
	require.Equal(t, uint32(0), ticketedMap["Sequence"])

	encoded, err := binarycodec.Encode(flat)
	require.NoError(t, err)
	blob, err := hex.DecodeString(encoded)
	require.NoError(t, err)
	parsed, err := tx.ParseFromBinary(blob)
	require.NoError(t, err)
	parsedBatch, ok := parsed.(*Batch)
	require.True(t, ok)
	parsedInner := parsedBatch.RawTransactions[0].RawTransaction.InnerTx
	require.NotNil(t, parsedInner)
	require.NotNil(t, parsedInner.GetCommon().Sequence)
	require.Zero(t, *parsedInner.GetCommon().Sequence)
	require.NotNil(t, parsedInner.GetCommon().TicketSequence)
	require.Equal(t, ticketSequence, *parsedInner.GetCommon().TicketSequence)

	originalHash, err := tx.ComputeTransactionHash(ticketed)
	require.NoError(t, err)
	parsedHash, err := tx.ComputeTransactionHash(parsedInner)
	require.NoError(t, err)
	require.Equal(t, originalHash, parsedHash)
}

func TestBatchBinaryParseRejectsInnerWithoutSigningPubKey(t *testing.T) {
	outer := NewBatch(testOuter)
	outer.Fee = "40"
	outer.SigningPubKey = "ED0000000000000000000000000000000000000000000000000000000000000000"
	outerSequence := uint32(1)
	outer.Sequence = &outerSequence
	flags := BatchFlagAllOrNothing
	outer.Flags = &flags
	outer.AddInnerTransaction(makeTestPayment())
	outer.AddInnerTransaction(makeTestPayment())

	flat, err := outer.Flatten()
	require.NoError(t, err)
	rawTransactions := flat["RawTransactions"].([]map[string]any)
	delete(rawTransactions[0]["RawTransaction"].(map[string]any), "SigningPubKey")
	encoded, err := binarycodec.Encode(flat)
	require.NoError(t, err)
	blob, err := hex.DecodeString(encoded)
	require.NoError(t, err)

	_, err = tx.ParseFromBinary(blob)
	require.Error(t, err)
	require.Contains(t, err.Error(), "SigningPubKey")
}

// Valid base58 r-addresses for white-box validation tests. Account fields must
// be decodable because Validate computes inner transaction hashes, which
// serialize the Account field.
const (
	testOuter   = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	testSigner1 = "r9cZA1mLK5R5Am25ArfXFmqgNwjZgnfk59"
	testSigner2 = "rB5Ux4Lv2nRx6eeoAAsZmtctnBQ2LiACnk"
)

type clawbackWithTopLevelMPTID struct {
	*clawback.Clawback
}

func (c *clawbackWithTopLevelMPTID) Flatten() (map[string]any, error) {
	fields, err := c.Clawback.Flatten()
	if err != nil {
		return nil, err
	}
	fields["MPTokenIssuanceID"] = "000000000000000000000001000000000000000000000001"
	return fields, nil
}

// Auto-incremented so successive inners hash uniquely (Batch.Validate rejects
// duplicates per rippled Batch.cpp:253-259).
var makeTestPaymentSeq uint32

func makeTestPayment() tx.Transaction {
	return makeTestPaymentFrom(testOuter)
}

// makeTestPaymentFrom builds an inner Payment whose account is `from`, so the
// caller controls whether the inner requires a BatchSigner (account != outer).
func makeTestPaymentFrom(from string) tx.Transaction {
	makeTestPaymentSeq++
	seq := makeTestPaymentSeq
	p := payment.NewPayment(from, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", tx.NewXRPAmount(1))
	p.Fee = "0"
	p.SigningPubKey = ""
	p.Sequence = &seq
	flags := tx.TfInnerBatchTxn
	p.Flags = &flags
	return p
}

// Batch Validation Tests
// Based on rippled Batch.cpp

func TestBatchValidation(t *testing.T) {
	// Helper to create a valid batch with minimum requirements
	makeValidBatch := func() *Batch {
		b := NewBatch(testOuter)
		b.AddInnerTransaction(makeTestPayment())
		b.AddInnerTransaction(makeTestPayment())
		flags := BatchFlagAllOrNothing
		b.Common.Flags = &flags
		return b
	}

	tests := []struct {
		name    string
		tx      *Batch
		wantErr bool
		errMsg  string
	}{
		// Valid cases
		{
			name:    "valid - basic batch with AllOrNothing",
			tx:      makeValidBatch(),
			wantErr: false,
		},
		{
			name: "valid - batch with OnlyOne flag",
			tx: func() *Batch {
				b := NewBatch(testOuter)
				b.AddInnerTransaction(makeTestPayment())
				b.AddInnerTransaction(makeTestPayment())
				flags := BatchFlagOnlyOne
				b.Common.Flags = &flags
				return b
			}(),
			wantErr: false,
		},
		{
			name: "valid - batch with UntilFailure flag",
			tx: func() *Batch {
				b := NewBatch(testOuter)
				b.AddInnerTransaction(makeTestPayment())
				b.AddInnerTransaction(makeTestPayment())
				flags := BatchFlagUntilFailure
				b.Common.Flags = &flags
				return b
			}(),
			wantErr: false,
		},
		{
			name: "valid - batch with Independent flag",
			tx: func() *Batch {
				b := NewBatch(testOuter)
				b.AddInnerTransaction(makeTestPayment())
				b.AddInnerTransaction(makeTestPayment())
				flags := BatchFlagIndependent
				b.Common.Flags = &flags
				return b
			}(),
			wantErr: false,
		},
		{
			name: "valid - maximum 8 transactions",
			tx: func() *Batch {
				b := NewBatch(testOuter)
				for range 8 {
					b.AddInnerTransaction(makeTestPayment())
				}
				flags := BatchFlagAllOrNothing
				b.Common.Flags = &flags
				return b
			}(),
			wantErr: false,
		},
		{
			// Signers cover the required inner accounts (rSigner1, rSigner2), so the
			// structural coverage rule passes. Validate is context-free and no longer
			// performs the cryptographic signature check — that moved to the engine's
			// signature stage so it honours SkipSignatureVerification — so unverifiable
			// signatures pass Validate here. The crypto rejection is covered by
			// TestVerifyBatchSignaturesRejectsBadSignatures and the integration suite.
			name: "valid - signers cover required inners (crypto checked by engine)",
			tx: func() *Batch {
				b := NewBatch(testOuter)
				b.AddInnerTransaction(makeTestPaymentFrom(testSigner1))
				b.AddInnerTransaction(makeTestPaymentFrom(testSigner2))
				flags := BatchFlagAllOrNothing
				b.Common.Flags = &flags
				b.BatchSigners = []BatchSigner{
					{BatchSigner: BatchSignerData{Account: testSigner1, SigningPubKey: "ABC", BatchTxnSignature: "DEF"}},
					{BatchSigner: BatchSignerData{Account: testSigner2, SigningPubKey: "GHI", BatchTxnSignature: "JKL"}},
				}
				return b
			}(),
			wantErr: false,
		},

		// Invalid cases - transaction count
		{
			name: "invalid - no transactions (empty array)",
			tx: func() *Batch {
				b := NewBatch(testOuter)
				flags := BatchFlagAllOrNothing
				b.Common.Flags = &flags
				return b
			}(),
			wantErr: true,
			errMsg:  "at least 2",
		},
		{
			name: "invalid - only 1 transaction",
			tx: func() *Batch {
				b := NewBatch(testOuter)
				b.AddInnerTransaction(makeTestPayment())
				flags := BatchFlagAllOrNothing
				b.Common.Flags = &flags
				return b
			}(),
			wantErr: true,
			errMsg:  "at least 2",
		},
		{
			name: "invalid - too many transactions (>8)",
			tx: func() *Batch {
				b := NewBatch(testOuter)
				for range 9 {
					b.AddInnerTransaction(makeTestPayment())
				}
				flags := BatchFlagAllOrNothing
				b.Common.Flags = &flags
				return b
			}(),
			wantErr: true,
			errMsg:  "exceeds 8",
		},

		// Invalid cases - flags
		{
			name: "invalid - no mode flag set",
			tx: func() *Batch {
				b := NewBatch(testOuter)
				b.AddInnerTransaction(makeTestPayment())
				b.AddInnerTransaction(makeTestPayment())
				flags := uint32(0)
				b.Common.Flags = &flags
				return b
			}(),
			wantErr: true,
			errMsg:  "exactly one",
		},
		{
			name: "invalid - multiple mode flags set",
			tx: func() *Batch {
				b := NewBatch(testOuter)
				b.AddInnerTransaction(makeTestPayment())
				b.AddInnerTransaction(makeTestPayment())
				flags := BatchFlagAllOrNothing | BatchFlagOnlyOne
				b.Common.Flags = &flags
				return b
			}(),
			wantErr: true,
			errMsg:  "exactly one",
		},
		{
			name: "invalid - all mode flags set",
			tx: func() *Batch {
				b := NewBatch(testOuter)
				b.AddInnerTransaction(makeTestPayment())
				b.AddInnerTransaction(makeTestPayment())
				flags := BatchFlagAllOrNothing | BatchFlagOnlyOne | BatchFlagUntilFailure | BatchFlagIndependent
				b.Common.Flags = &flags
				return b
			}(),
			wantErr: true,
			errMsg:  "exactly one",
		},

		// Invalid cases - nil inner transaction
		{
			name: "invalid - nil inner transaction",
			tx: func() *Batch {
				b := NewBatch(testOuter)
				b.RawTransactions = []RawTransaction{
					{RawTransaction: RawTransactionData{InnerTx: makeTestPayment()}},
					{RawTransaction: RawTransactionData{InnerTx: nil}}, // nil
				}
				flags := BatchFlagAllOrNothing
				b.Common.Flags = &flags
				return b
			}(),
			wantErr: true,
			errMsg:  "inner transaction cannot be nil",
		},
		{
			name: "invalid - Clawback field outside template",
			tx: func() *Batch {
				b := NewBatch(testOuter)
				b.AddInnerTransaction(&clawbackWithTopLevelMPTID{Clawback: clawback.NewClawback(
					testOuter,
					tx.NewIssuedAmount(1, 0, "USD", testSigner1),
				)})
				b.AddInnerTransaction(makeTestPayment())
				flags := BatchFlagAllOrNothing
				b.Common.Flags = &flags
				return b
			}(),
			wantErr: true,
			errMsg:  "temINVALID_INNER_BATCH",
		},

		// Invalid cases - batch signers
		{
			name: "invalid - too many batch signers",
			tx: func() *Batch {
				b := makeValidBatch()
				for range MaxBatchSigners + 1 {
					b.BatchSigners = append(b.BatchSigners, BatchSigner{
						BatchSigner: BatchSignerData{Account: testSigner1, SigningPubKey: "ABC"},
					})
				}
				return b
			}(),
			wantErr: true,
			errMsg:  "exceeds 24",
		},
		{
			// rSigner1's inner makes it a required signer, so the first signer entry
			// passes the coverage check and the duplicate second entry is caught.
			name: "invalid - duplicate batch signer",
			tx: func() *Batch {
				b := NewBatch(testOuter)
				b.AddInnerTransaction(makeTestPaymentFrom(testSigner1))
				b.AddInnerTransaction(makeTestPayment())
				flags := BatchFlagAllOrNothing
				b.Common.Flags = &flags
				b.BatchSigners = []BatchSigner{
					{BatchSigner: BatchSignerData{Account: testSigner1, SigningPubKey: "ABC"}},
					{BatchSigner: BatchSignerData{Account: testSigner1, SigningPubKey: "ABC"}}, // duplicate
				}
				return b
			}(),
			wantErr: true,
			errMsg:  "duplicate",
		},
		{
			name: "invalid - batch signer is outer account",
			tx: func() *Batch {
				b := makeValidBatch()
				b.BatchSigners = []BatchSigner{
					{BatchSigner: BatchSignerData{Account: testOuter, SigningPubKey: "ABC"}}, // same as outer
				}
				return b
			}(),
			wantErr: true,
			errMsg:  "outer account",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.tx.Common.Fee = "12"
			seq := uint32(1)
			tt.tx.Common.Sequence = &seq

			err := tt.tx.Validate()
			if err == nil {
				err = tt.tx.PreflightSigValidated()
			}
			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// Flatten Tests

func TestBatchFlatten(t *testing.T) {
	t.Run("basic batch", func(t *testing.T) {
		b := NewBatch(testOuter)
		b.AddInnerTransaction(makeTestPayment())
		b.AddInnerTransaction(makeTestPayment())

		flat, err := b.Flatten()
		require.NoError(t, err)

		assert.Equal(t, testOuter, flat["Account"])
		assert.Equal(t, "Batch", flat["TransactionType"])

		rawTxns, ok := flat["RawTransactions"].([]map[string]any)
		require.True(t, ok)
		assert.Len(t, rawTxns, 2)

		// Each element should have a "RawTransaction" key with the inner tx map
		for _, rtMap := range rawTxns {
			innerTx, ok := rtMap["RawTransaction"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "Payment", innerTx["TransactionType"])
		}
	})

	t.Run("batch with signers", func(t *testing.T) {
		b := NewBatch(testOuter)
		b.AddInnerTransaction(makeTestPayment())
		b.AddInnerTransaction(makeTestPayment())
		b.BatchSigners = []BatchSigner{
			{BatchSigner: BatchSignerData{Account: testSigner1, SigningPubKey: "ABC", BatchTxnSignature: "DEF"}},
		}

		flat, err := b.Flatten()
		require.NoError(t, err)

		signers, ok := flat["BatchSigners"].([]map[string]any)
		require.True(t, ok)
		assert.Len(t, signers, 1)
	})

	t.Run("rejects disallowed Clawback field", func(t *testing.T) {
		b := NewBatch(testOuter)
		b.AddInnerTransaction(&clawbackWithTopLevelMPTID{Clawback: clawback.NewClawback(
			testOuter,
			tx.NewIssuedAmount(1, 0, "USD", testSigner1),
		)})

		_, err := b.Flatten()
		require.EqualError(t, err, "temMALFORMED: invalid inner transaction 0: Field 'MPTokenIssuanceID' found in disallowed location.")
	})
}

// Constructor Tests

func TestBatchConstructors(t *testing.T) {
	t.Run("NewBatch", func(t *testing.T) {
		b := NewBatch(testOuter)
		require.NotNil(t, b)
		assert.Equal(t, testOuter, b.Account)
		assert.Equal(t, tx.TypeBatch, b.TxType())
		assert.Empty(t, b.RawTransactions)
		assert.Empty(t, b.BatchSigners)
	})
}

// AddInnerTransaction Test

func TestBatchAddInnerTransaction(t *testing.T) {
	b := NewBatch(testOuter)

	tx1 := makeTestPayment()
	tx2 := makeTestPayment()
	b.AddInnerTransaction(tx1)
	b.AddInnerTransaction(tx2)

	require.Len(t, b.RawTransactions, 2)
	assert.Equal(t, tx1, b.RawTransactions[0].RawTransaction.InnerTx)
	assert.Equal(t, tx2, b.RawTransactions[1].RawTransaction.InnerTx)
}

// Amendment Tests

func TestBatchRequiredAmendments(t *testing.T) {
	b := NewBatch(testOuter)
	amendments := b.RequiredAmendments()
	assert.Contains(t, amendments, amendment.FeatureBatchV1_1)
}

// Constants Tests

func TestBatchConstants(t *testing.T) {
	assert.Equal(t, 8, MaxBatchTransactions)
	assert.Equal(t, uint32(0x00010000), BatchFlagAllOrNothing)
	assert.Equal(t, uint32(0x00020000), BatchFlagOnlyOne)
	assert.Equal(t, uint32(0x00040000), BatchFlagUntilFailure)
	assert.Equal(t, uint32(0x00080000), BatchFlagIndependent)

	// Pins finding Batch-mask-value: tfBatchMask matches rippled TxFlags.h
	// (0x7FF0FFFF) — it permits tfFullyCanonicalSig and the four mode bits, and
	// rejects tfInnerBatchTxn on the outer Batch.
	assert.Equal(t, uint32(0x7FF0FFFF), tfBatchMask)
	assert.Zero(t, tx.TfFullyCanonicalSig&tfBatchMask, "tfFullyCanonicalSig must be allowed")
	assert.NotZero(t, tx.TfInnerBatchTxn&tfBatchMask, "tfInnerBatchTxn must be rejected on the outer")
	for _, mode := range []uint32{BatchFlagAllOrNothing, BatchFlagOnlyOne, BatchFlagUntilFailure, BatchFlagIndependent} {
		assert.Zero(t, mode&tfBatchMask, "mode flag must be allowed")
	}
}

// TestCalculateMinimumFee_SingleSignBaseline pins the common case
// (single-signed inners, no BatchSigners): the formula degenerates
// to (numInner + 2) * baseFee.
func TestCalculateMinimumFee_SingleSignBaseline(t *testing.T) {
	b := NewBatch(testOuter)
	b.AddInnerTransaction(makeTestPayment())
	b.AddInnerTransaction(makeTestPayment())
	require.Equal(t, uint64(40), b.CalculateMinimumFee(nil, tx.EngineConfig{BaseFee: 10}), "2 inners + no signers")

	b3 := NewBatch(testOuter)
	b3.AddInnerTransaction(makeTestPayment())
	b3.AddInnerTransaction(makeTestPayment())
	b3.AddInnerTransaction(makeTestPayment())
	require.Equal(t, uint64(50), b3.CalculateMinimumFee(nil, tx.EngineConfig{BaseFee: 10}), "3 inners + no signers")
}

func TestCalculateMinimumFee_DispatchesInnerCustomFee(t *testing.T) {
	for _, tc := range []struct {
		name         string
		counterparty *tx.CounterpartySignature
		expected     uint64
	}{
		{"absent", nil, 30},
		{"single", &tx.CounterpartySignature{TxnSignature: "AA"}, 40},
		{"multisigned", &tx.CounterpartySignature{Signers: make([]tx.SignerWrapper, 2)}, 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBatch(testOuter)
			loanSet := lending.NewLoanSet(testOuter, strings.Repeat("1", 64), "1")
			loanSet.GetCommon().CounterpartySignature = tc.counterparty
			b.AddInnerTransaction(loanSet)
			require.Equal(t, tc.expected, b.CalculateMinimumFee(nil, tx.EngineConfig{BaseFee: 10}))
		})
	}
}

// TestCalculateMinimumFee_DirectSignedBatchSigners pins
// Batch.cpp:130-131 — each BatchSigner with a direct BatchTxnSignature
// adds one base fee.
func TestCalculateMinimumFee_DirectSignedBatchSigners(t *testing.T) {
	b := NewBatch(testOuter)
	b.AddInnerTransaction(makeTestPayment())
	b.AddInnerTransaction(makeTestPayment())
	b.BatchSigners = []BatchSigner{
		{BatchSigner: BatchSignerData{Account: "rSignerA", BatchTxnSignature: "AB"}},
		{BatchSigner: BatchSignerData{Account: "rSignerB", BatchTxnSignature: "CD"}},
	}
	// batchBase=20 + txnFees=20 + signerFees=2*10 = 60
	require.Equal(t, uint64(60), b.CalculateMinimumFee(nil, tx.EngineConfig{BaseFee: 10}))
}

// TestCalculateMinimumFee_MultiSignBatchSigner pins
// Batch.cpp:132-134 — a multi-signed BatchSigner (no direct
// TxnSignature, populated Signers array) contributes
// len(Signers) * baseFee, NOT just one base fee.
func TestCalculateMinimumFee_MultiSignBatchSigner(t *testing.T) {
	b := NewBatch(testOuter)
	b.AddInnerTransaction(makeTestPayment())
	b.AddInnerTransaction(makeTestPayment())
	b.BatchSigners = []BatchSigner{{
		BatchSigner: BatchSignerData{
			Account: "rSignerA",
			Signers: []tx.SignerWrapper{
				{Signer: tx.Signer{Account: "rNested1", SigningPubKey: "01", TxnSignature: "AA"}},
				{Signer: tx.Signer{Account: "rNested2", SigningPubKey: "02", TxnSignature: "BB"}},
				{Signer: tx.Signer{Account: "rNested3", SigningPubKey: "03", TxnSignature: "CC"}},
			},
		},
	}}
	// batchBase=20 + txnFees=20 + signerFees=3*10 = 70
	require.Equal(t, uint64(70), b.CalculateMinimumFee(nil, tx.EngineConfig{BaseFee: 10}))
}

func TestBatchRequiredSignersUseInnerAuthorizers(t *testing.T) {
	t.Run("delegate authorizes outer account inner", func(t *testing.T) {
		b := NewBatch(testOuter)
		inner := makeTestPayment()
		inner.GetCommon().Delegate = testSigner2
		b.AddInnerTransaction(inner)
		b.AddInnerTransaction(makeTestPayment())
		b.SetFlags(BatchFlagAllOrNothing)

		require.NoError(t, b.Validate())
		require.ErrorIs(t, b.PreflightSigValidated(), ErrBatchMissingSigner)

		b.BatchSigners = []BatchSigner{{BatchSigner: BatchSignerData{
			Account:           testSigner2,
			SigningPubKey:     "01",
			BatchTxnSignature: "AA",
		}}}
		require.NoError(t, b.Validate())
		require.NoError(t, b.PreflightSigValidated())
	})
}

func TestBatchSignersMustBeStrictlyOrdered(t *testing.T) {
	b := NewBatch(testOuter)
	b.AddInnerTransaction(makeTestPaymentFrom(testSigner1))
	b.AddInnerTransaction(makeTestPaymentFrom(testSigner2))
	b.SetFlags(BatchFlagAllOrNothing)
	b.BatchSigners = []BatchSigner{
		{BatchSigner: BatchSignerData{Account: testSigner2, SigningPubKey: "01"}},
		{BatchSigner: BatchSignerData{Account: testSigner1, SigningPubKey: "02"}},
	}

	require.NoError(t, b.Validate())
	require.ErrorIs(t, b.PreflightSigValidated(), ErrBatchUnsortedSigner)
}

// TestCalculateMinimumFee_MultiSignedInner pins
// Batch.cpp:87-100 — inner transactions count their own per-tx
// calculateBaseFee, so a multi-signed inner pays (1+n) * baseFee
// instead of one base fee.
func TestCalculateMinimumFee_MultiSignedInner(t *testing.T) {
	b := NewBatch(testOuter)
	b.AddInnerTransaction(makeTestPayment())
	multiInner := makeTestPayment()
	multiInner.GetCommon().Signers = []tx.SignerWrapper{
		{Signer: tx.Signer{Account: "rNested1", SigningPubKey: "01", TxnSignature: "AA"}},
		{Signer: tx.Signer{Account: "rNested2", SigningPubKey: "02", TxnSignature: "BB"}},
	}
	b.AddInnerTransaction(multiInner)
	// batchBase=20 + txnFees=(10 + 30) + signerFees=0 = 60
	require.Equal(t, uint64(60), b.CalculateMinimumFee(nil, tx.EngineConfig{BaseFee: 10}))
}

// TestCalculateMinimumFee_OuterMultiSign pins
// Batch.cpp:60-70 — Transactor::calculateBaseFee charges
// (1 + outerSigners) * baseFee for the outer Batch tx itself,
// in addition to the view.fees().base added by the Batch wrapper.
func TestCalculateMinimumFee_OuterMultiSign(t *testing.T) {
	b := NewBatch(testOuter)
	b.AddInnerTransaction(makeTestPayment())
	b.Common.Signers = []tx.SignerWrapper{
		{Signer: tx.Signer{Account: "rOuter1", SigningPubKey: "01", TxnSignature: "AA"}},
		{Signer: tx.Signer{Account: "rOuter2", SigningPubKey: "02", TxnSignature: "BB"}},
	}
	// batchBase = 10 + (1 + 2)*10 = 40; txnFees = 10; signerFees = 0 → 50
	require.Equal(t, uint64(50), b.CalculateMinimumFee(nil, tx.EngineConfig{BaseFee: 10}))
}

func TestCalculateMinimumFee_InvalidStructureFallsBackAndPreclaimRejects(t *testing.T) {
	outer := NewBatch(testOuter)
	innerBatch := NewBatch("rInner")
	outer.AddInnerTransaction(innerBatch)
	config := tx.EngineConfig{BaseFee: 10}
	require.Equal(t, uint64(10), outer.CalculateMinimumFee(nil, config))
	require.Equal(t, ter.TecINSUFF_FEE, outer.Preclaim(nil, config))
}

func TestCalculateMinimumFeeCountsPresentEmptyBatchTxnSignature(t *testing.T) {
	outer := NewBatch(testOuter)
	outer.Fee = "50"
	seq := uint32(1)
	outer.Sequence = &seq
	flags := BatchFlagAllOrNothing
	outer.Flags = &flags
	outer.AddInnerTransaction(makeTestPayment())
	outer.AddInnerTransaction(makeTestPayment())
	flat, err := outer.Flatten()
	require.NoError(t, err)
	flat["BatchSigners"] = []map[string]any{{"BatchSigner": map[string]any{
		"Account":       testSigner1,
		"SigningPubKey": "",
		"TxnSignature":  "",
	}}}
	encoded, err := binarycodec.Encode(flat)
	require.NoError(t, err)
	blob, err := hex.DecodeString(encoded)
	require.NoError(t, err)
	parsed, err := tx.ParseFromBinary(blob)
	require.NoError(t, err)
	parsedBatch := parsed.(*Batch)
	require.True(t, parsedBatch.BatchSigners[0].BatchSigner.hasTxnSignature())
	require.Equal(t, uint64(50), parsedBatch.CalculateMinimumFee(nil, tx.EngineConfig{BaseFee: 10}))
}

func TestInnerExplicitEmptySignatureFieldsAreRejected(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		value   any
		wantErr error
	}{
		{name: "TxnSignature", field: "TxnSignature", value: "", wantErr: ErrBatchInnerHasTxnSignature},
		{name: "Signers", field: "Signers", value: []any{}, wantErr: ErrBatchInnerHasSigners},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outer := NewBatch(testOuter)
			outer.Fee = "40"
			outer.SetSequence(1)
			outer.SetFlags(BatchFlagAllOrNothing)
			outer.AddInnerTransaction(makeTestPayment())
			outer.AddInnerTransaction(makeTestPayment())
			fields, err := outer.Flatten()
			require.NoError(t, err)
			rawTransactions := fields["RawTransactions"].([]map[string]any)
			inner := rawTransactions[0]["RawTransaction"].(map[string]any)
			inner[test.field] = test.value
			encoded, err := binarycodec.Encode(fields)
			require.NoError(t, err)
			blob, err := hex.DecodeString(encoded)
			require.NoError(t, err)
			parsed, err := tx.ParseFromBinary(blob)
			require.NoError(t, err)
			require.ErrorIs(t, parsed.(*Batch).Validate(), test.wantErr)
		})
	}
}

func TestBindRawBytesRejectsExplicitEmptyInnerFieldMismatch(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value any
	}{
		{name: "TxnSignature", field: "TxnSignature", value: ""},
		{name: "Signers", field: "Signers", value: []map[string]any{}},
		{name: "CounterpartySignature", field: "CounterpartySignature", value: map[string]any{
			"SigningPubKey": "",
			"TxnSignature":  "",
		}},
		{name: "SponsorSignature", field: "SponsorSignature", value: map[string]any{
			"SigningPubKey": "",
			"TxnSignature":  "",
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outer := NewBatch(testOuter)
			outer.SetFlags(BatchFlagAllOrNothing)
			outer.AddInnerTransaction(makeTestPayment())
			outer.AddInnerTransaction(makeTestPayment())
			fields, err := outer.Flatten()
			require.NoError(t, err)
			rawTransactions := fields["RawTransactions"].([]map[string]any)
			rawTransactions[0]["RawTransaction"].(map[string]any)[test.field] = test.value
			encoded, err := binarycodec.Encode(fields)
			require.NoError(t, err)
			blob, err := hex.DecodeString(encoded)
			require.NoError(t, err)
			require.Error(t, tx.BindRawBytes(outer, blob))
			require.Empty(t, outer.GetRawBytes())
		})
	}
}
