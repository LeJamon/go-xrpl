//go:build ignore

package tx

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

// TestMultiSignature tests multi-signature functionality.
// These tests are translated from rippled's MultiSign_test.cpp

// TestMultiSigFeeCalculation tests the fee calculation for multi-signed transactions.
// Reference: rippled MultiSign_test.cpp::test_fee
func TestMultiSigFeeCalculation(t *testing.T) {
	tests := []struct {
		name       string
		baseFee    uint64
		numSigners int
		expected   uint64
	}{
		{
			name:       "single signer fee",
			baseFee:    10,
			numSigners: 1,
			expected:   20, // 10 * (1 + 1) = 20
		},
		{
			name:       "two signers fee",
			baseFee:    10,
			numSigners: 2,
			expected:   30, // 10 * (1 + 2) = 30
		},
		{
			name:       "eight signers fee (max without ExpandedSignerList)",
			baseFee:    10,
			numSigners: 8,
			expected:   90, // 10 * (1 + 8) = 90
		},
		{
			name:       "32 signers fee (max with ExpandedSignerList)",
			baseFee:    10,
			numSigners: 32,
			expected:   330, // 10 * (1 + 32) = 330
		},
		{
			name:       "realistic base fee",
			baseFee:    12,
			numSigners: 3,
			expected:   48, // 12 * (1 + 3) = 48
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CalculateMultiSigFee(tt.baseFee, tt.numSigners)
			if result != tt.expected {
				t.Errorf("CalculateMultiSigFee(%d, %d) = %d, want %d",
					tt.baseFee, tt.numSigners, result, tt.expected)
			}
		})
	}
}

// TestMultiSigFeeDropsCalculation tests the fee calculation with string drops.
func TestMultiSigFeeDropsCalculation(t *testing.T) {
	tests := []struct {
		name        string
		baseFee     string
		numSigners  int
		expected    string
		expectError bool
	}{
		{
			name:       "valid fee calculation",
			baseFee:    "10",
			numSigners: 2,
			expected:   "30",
		},
		{
			name:       "standard base fee",
			baseFee:    "12",
			numSigners: 5,
			expected:   "72",
		},
		{
			name:        "invalid base fee string",
			baseFee:     "invalid",
			numSigners:  2,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := CalculateMultiSigFeeDrops(tt.baseFee, tt.numSigners)
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if result != tt.expected {
					t.Errorf("CalculateMultiSigFeeDrops(%s, %d) = %s, want %s",
						tt.baseFee, tt.numSigners, result, tt.expected)
				}
			}
		})
	}
}

// TestIsMultiSigned tests the detection of multi-signed transactions.
func TestIsMultiSigned(t *testing.T) {
	tests := []struct {
		name     string
		common   Common
		expected bool
	}{
		{
			name: "single signed transaction",
			common: Common{
				Account:         "rAlice",
				TransactionType: "AccountSet",
				SigningPubKey:   "ED1234567890ABCDEF",
				TxnSignature:    "30440220...",
			},
			expected: false,
		},
		{
			name: "multi-signed transaction",
			common: Common{
				Account:         "rAlice",
				TransactionType: "AccountSet",
				SigningPubKey:   "", // Empty for multi-sig
				Signers: []SignerWrapper{
					{Signer: Signer{Account: "rBob", SigningPubKey: "ED...", TxnSignature: "..."}},
				},
			},
			expected: true,
		},
		{
			name: "unsigned transaction",
			common: Common{
				Account:         "rAlice",
				TransactionType: "AccountSet",
				SigningPubKey:   "",
			},
			expected: false,
		},
		{
			name: "both single and multi (invalid but should detect multi)",
			common: Common{
				Account:         "rAlice",
				TransactionType: "AccountSet",
				SigningPubKey:   "ED1234567890ABCDEF", // Non-empty
				Signers: []SignerWrapper{
					{Signer: Signer{Account: "rBob", SigningPubKey: "ED...", TxnSignature: "..."}},
				},
			},
			expected: false, // SigningPubKey is not empty, so not multi-signed
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a minimal transaction with the common fields
			payment := &AccountSet{
				BaseTx: BaseTx{Common: tt.common},
			}
			result := IsMultiSigned(payment)
			if result != tt.expected {
				t.Errorf("IsMultiSigned() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestGetTransactionSignerCount tests counting signers in a transaction.
func TestGetTransactionSignerCount(t *testing.T) {
	tests := []struct {
		name     string
		signers  []SignerWrapper
		expected int
	}{
		{
			name:     "no signers",
			signers:  nil,
			expected: 0,
		},
		{
			name: "one signer",
			signers: []SignerWrapper{
				{Signer: Signer{Account: "rBob"}},
			},
			expected: 1,
		},
		{
			name: "three signers",
			signers: []SignerWrapper{
				{Signer: Signer{Account: "rBob"}},
				{Signer: Signer{Account: "rCharlie"}},
				{Signer: Signer{Account: "rDave"}},
			},
			expected: 3,
		},
		{
			name: "eight signers (max without ExpandedSignerList)",
			signers: []SignerWrapper{
				{Signer: Signer{Account: "rA"}},
				{Signer: Signer{Account: "rB"}},
				{Signer: Signer{Account: "rC"}},
				{Signer: Signer{Account: "rD"}},
				{Signer: Signer{Account: "rE"}},
				{Signer: Signer{Account: "rF"}},
				{Signer: Signer{Account: "rG"}},
				{Signer: Signer{Account: "rH"}},
			},
			expected: 8,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payment := &AccountSet{
				BaseTx: BaseTx{
					Common: Common{
						Account:         "rAlice",
						TransactionType: "AccountSet",
						Signers:         tt.signers,
					},
				},
			}
			result := GetTransactionSignerCount(payment)
			if result != tt.expected {
				t.Errorf("GetTransactionSignerCount() = %d, want %d", result, tt.expected)
			}
		})
	}
}

// Teststate.SignerListInfo tests the state.SignerListInfo structure.
func TestsleSignerListInfo(t *testing.T) {
	t.Run("basic signer list", func(t *testing.T) {
		signerList := state.SignerListInfo{
			SignerQuorum: 2,
			SignerListID: 0,
			SignerEntries: []AccountSignerEntry{
				{Account: "rBob", SignerWeight: 1},
				{Account: "rCharlie", SignerWeight: 1},
			},
		}

		if signerList.SignerQuorum != 2 {
			t.Errorf("SignerQuorum = %d, want 2", signerList.SignerQuorum)
		}
		if len(signerList.SignerEntries) != 2 {
			t.Errorf("SignerEntries length = %d, want 2", len(signerList.SignerEntries))
		}
	})

	t.Run("signer list with different weights", func(t *testing.T) {
		signerList := state.SignerListInfo{
			SignerQuorum: 4,
			SignerListID: 0,
			SignerEntries: []AccountSignerEntry{
				{Account: "rBob", SignerWeight: 3},
				{Account: "rCharlie", SignerWeight: 4},
			},
		}

		// Verify weight sum can meet quorum
		var totalWeight uint16
		for _, entry := range signerList.SignerEntries {
			totalWeight += entry.SignerWeight
		}
		if totalWeight < uint16(signerList.SignerQuorum) {
			t.Errorf("total weight %d cannot meet quorum %d", totalWeight, signerList.SignerQuorum)
		}
	})
}

// TestMultiSignErrors tests multi-signature error codes and messages.
// Reference: rippled MultiSign_test.cpp::test_badSignatureText and test_noMultiSigners
func TestMultiSignErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "not multi-signing error",
			err:      ErrNotMultiSigning,
			expected: "tefNOT_MULTI_SIGNING: account is not configured for multi-signing",
		},
		{
			name:     "bad quorum error",
			err:      ErrBadQuorum,
			expected: "tefBAD_QUORUM: signers failed to meet quorum",
		},
		{
			name:     "bad signature error",
			err:      ErrBadSignature,
			expected: "tefBAD_SIGNATURE: invalid signer or signature",
		},
		{
			name:     "master disabled error",
			err:      ErrMasterDisabled,
			expected: "tefMASTER_DISABLED: master key is disabled for this signer",
		},
		{
			name:     "no signers error",
			err:      ErrNoSigners,
			expected: "multi-signed transaction has no signers",
		},
		{
			name:     "duplicate signer error",
			err:      ErrDuplicateSigner,
			expected: "duplicate signer in transaction",
		},
		{
			name:     "signers not sorted error",
			err:      ErrSignersNotSorted,
			expected: "signers must be sorted by account",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Error() != tt.expected {
				t.Errorf("error message = %q, want %q", tt.err.Error(), tt.expected)
			}
		})
	}
}

// TestAddMultiSigner tests adding signers to a transaction.
// Reference: rippled MultiSign_test.cpp::test_misorderedSigners
func TestAddMultiSigner(t *testing.T) {
	t.Run("add signers in sorted order", func(t *testing.T) {
		payment := &AccountSet{
			BaseTx: *NewBaseTx(TypeAccountSet, "rAlice"),
		}

		// Add signers - they should be auto-sorted
		err := AddMultiSigner(payment, "rCharlie", "ED111...", "sig1")
		if err != nil {
			t.Fatalf("AddMultiSigner failed: %v", err)
		}

		err = AddMultiSigner(payment, "rBob", "ED222...", "sig2")
		if err != nil {
			t.Fatalf("AddMultiSigner failed: %v", err)
		}

		err = AddMultiSigner(payment, "rDave", "ED333...", "sig3")
		if err != nil {
			t.Fatalf("AddMultiSigner failed: %v", err)
		}

		// Verify signers are sorted
		common := payment.GetCommon()
		if len(common.Signers) != 3 {
			t.Fatalf("expected 3 signers, got %d", len(common.Signers))
		}

		// Check that SigningPubKey was cleared
		if common.SigningPubKey != "" {
			t.Error("SigningPubKey should be empty for multi-signed transaction")
		}

		// Verify sorted order (rBob < rCharlie < rDave lexicographically)
		accounts := []string{
			common.Signers[0].Signer.Account,
			common.Signers[1].Signer.Account,
			common.Signers[2].Signer.Account,
		}

		for i := 0; i < len(accounts)-1; i++ {
			if accounts[i] >= accounts[i+1] {
				t.Errorf("signers not sorted: %s >= %s", accounts[i], accounts[i+1])
			}
		}
	})

	t.Run("reject duplicate signer", func(t *testing.T) {
		payment := &AccountSet{
			BaseTx: *NewBaseTx(TypeAccountSet, "rAlice"),
		}

		err := AddMultiSigner(payment, "rCharlie", "ED111...", "sig1")
		if err != nil {
			t.Fatalf("first AddMultiSigner failed: %v", err)
		}

		// Try to add the same signer again
		err = AddMultiSigner(payment, "rCharlie", "ED111...", "sig1")
		if err != ErrDuplicateSigner {
			t.Errorf("expected ErrDuplicateSigner, got %v", err)
		}
	})
}

// TestAccountSignerEntry tests the AccountSignerEntry structure.
func TestAccountSignerEntry(t *testing.T) {
	tests := []struct {
		name    string
		entry   AccountSignerEntry
		wantAcc string
		wantWt  uint16
	}{
		{
			name: "basic entry",
			entry: AccountSignerEntry{
				Account:      "rBob",
				SignerWeight: 1,
			},
			wantAcc: "rBob",
			wantWt:  1,
		},
		{
			name: "entry with max weight",
			entry: AccountSignerEntry{
				Account:      "rCharlie",
				SignerWeight: 0xFFFF,
			},
			wantAcc: "rCharlie",
			wantWt:  0xFFFF,
		},
		{
			name: "entry with wallet locator",
			entry: AccountSignerEntry{
				Account:       "rDave",
				SignerWeight:  1,
				WalletLocator: "0102030405060708010203040506070801020304050607080102030405060708",
			},
			wantAcc: "rDave",
			wantWt:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.entry.Account != tt.wantAcc {
				t.Errorf("Account = %s, want %s", tt.entry.Account, tt.wantAcc)
			}
			if tt.entry.SignerWeight != tt.wantWt {
				t.Errorf("SignerWeight = %d, want %d", tt.entry.SignerWeight, tt.wantWt)
			}
		})
	}
}

// TestQuorumCalculation tests quorum calculation logic.
// Reference: rippled MultiSign_test.cpp::test_phantomSigners
func TestQuorumCalculation(t *testing.T) {
	tests := []struct {
		name          string
		quorum        uint32
		signerWeights []uint16
		meetsQuorum   bool
	}{
		{
			name:          "single signer meets quorum",
			quorum:        1,
			signerWeights: []uint16{1},
			meetsQuorum:   true,
		},
		{
			name:          "single signer fails quorum",
			quorum:        2,
			signerWeights: []uint16{1},
			meetsQuorum:   false,
		},
		{
			name:          "two signers meet quorum",
			quorum:        2,
			signerWeights: []uint16{1, 1},
			meetsQuorum:   true,
		},
		{
			name:          "weighted signers meet quorum",
			quorum:        4,
			signerWeights: []uint16{3, 4},
			meetsQuorum:   true,
		},
		{
			name:          "weighted signers fail quorum",
			quorum:        8,
			signerWeights: []uint16{3, 4},
			meetsQuorum:   false,
		},
		{
			name:          "max weight signers",
			quorum:        0x3FFFC,
			signerWeights: []uint16{0xFFFF, 0xFFFF, 0xFFFF, 0xFFFF},
			meetsQuorum:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var totalWeight uint32
			for _, w := range tt.signerWeights {
				totalWeight += uint32(w)
			}
			result := totalWeight >= tt.quorum
			if result != tt.meetsQuorum {
				t.Errorf("quorum check: total=%d, quorum=%d, got %v, want %v",
					totalWeight, tt.quorum, result, tt.meetsQuorum)
			}
		})
	}
}

// Benchmark tests for multi-signature operations
func BenchmarkCalculateMultiSigFee(b *testing.B) {
	for i := 0; i < b.N; i++ {
		CalculateMultiSigFee(10, 8)
	}
}

func BenchmarkIsMultiSigned(b *testing.B) {
	payment := &AccountSet{
		BaseTx: BaseTx{
			Common: Common{
				Account:         "rAlice",
				TransactionType: "AccountSet",
				SigningPubKey:   "",
				Signers: []SignerWrapper{
					{Signer: Signer{Account: "rBob", SigningPubKey: "ED...", TxnSignature: "..."}},
				},
			},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		IsMultiSigned(payment)
	}
}
