package entry

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestLedgerSpecificFlags pins every Lsf* constant against rippled. These flags
// are scoped per ledger entry type, so identical numeric values are reused
// across types; the golden table records the expected value for each name.
// Reference: rippled/include/xrpl/protocol/LedgerFormats.h (LedgerSpecificFlags)
func TestLedgerSpecificFlags(t *testing.T) {
	tests := []struct {
		name string
		got  uint32
		want uint32
	}{
		// ltACCOUNT_ROOT
		{"LsfPasswordSpent", LsfPasswordSpent, 0x00010000},
		{"LsfRequireDestTag", LsfRequireDestTag, 0x00020000},
		{"LsfRequireAuth", LsfRequireAuth, 0x00040000},
		{"LsfDisallowXRP", LsfDisallowXRP, 0x00080000},
		{"LsfDisableMaster", LsfDisableMaster, 0x00100000},
		{"LsfNoFreeze", LsfNoFreeze, 0x00200000},
		{"LsfGlobalFreeze", LsfGlobalFreeze, 0x00400000},
		{"LsfDefaultRipple", LsfDefaultRipple, 0x00800000},
		{"LsfDepositAuth", LsfDepositAuth, 0x01000000},
		{"LsfDisallowIncomingNFTokenOffer", LsfDisallowIncomingNFTokenOffer, 0x04000000},
		{"LsfDisallowIncomingCheck", LsfDisallowIncomingCheck, 0x08000000},
		{"LsfDisallowIncomingPayChan", LsfDisallowIncomingPayChan, 0x10000000},
		{"LsfDisallowIncomingTrustline", LsfDisallowIncomingTrustline, 0x20000000},
		{"LsfAllowTrustLineLocking", LsfAllowTrustLineLocking, 0x40000000},
		{"LsfAllowTrustLineClawback", LsfAllowTrustLineClawback, 0x80000000},

		// ltOFFER
		{"LsfPassive", LsfPassive, 0x00010000},
		{"LsfSell", LsfSell, 0x00020000},
		{"LsfHybrid", LsfHybrid, 0x00040000},

		// ltRIPPLE_STATE
		{"LsfLowReserve", LsfLowReserve, 0x00010000},
		{"LsfHighReserve", LsfHighReserve, 0x00020000},
		{"LsfLowAuth", LsfLowAuth, 0x00040000},
		{"LsfHighAuth", LsfHighAuth, 0x00080000},
		{"LsfLowNoRipple", LsfLowNoRipple, 0x00100000},
		{"LsfHighNoRipple", LsfHighNoRipple, 0x00200000},
		{"LsfLowFreeze", LsfLowFreeze, 0x00400000},
		{"LsfHighFreeze", LsfHighFreeze, 0x00800000},
		{"LsfAMMNode", LsfAMMNode, 0x01000000},
		{"LsfLowDeepFreeze", LsfLowDeepFreeze, 0x02000000},
		{"LsfHighDeepFreeze", LsfHighDeepFreeze, 0x04000000},

		// ltSIGNER_LIST
		{"LsfOneOwnerCount", LsfOneOwnerCount, 0x00010000},

		// ltDIR_NODE
		{"LsfNFTokenBuyOffers", LsfNFTokenBuyOffers, 0x00000001},
		{"LsfNFTokenSellOffers", LsfNFTokenSellOffers, 0x00000002},

		// ltNFTOKEN_OFFER
		{"LsfSellNFToken", LsfSellNFToken, 0x00000001},

		// ltMPTOKEN_ISSUANCE
		{"LsfMPTLocked", LsfMPTLocked, 0x00000001},
		{"LsfMPTCanLock", LsfMPTCanLock, 0x00000002},
		{"LsfMPTRequireAuth", LsfMPTRequireAuth, 0x00000004},
		{"LsfMPTCanEscrow", LsfMPTCanEscrow, 0x00000008},
		{"LsfMPTCanTrade", LsfMPTCanTrade, 0x00000010},
		{"LsfMPTCanTransfer", LsfMPTCanTransfer, 0x00000020},
		{"LsfMPTCanClawback", LsfMPTCanClawback, 0x00000040},
		{"LsmfMPTCanMutateCanLock", LsmfMPTCanMutateCanLock, 0x00000002},
		{"LsmfMPTCanMutateRequireAuth", LsmfMPTCanMutateRequireAuth, 0x00000004},
		{"LsmfMPTCanMutateCanEscrow", LsmfMPTCanMutateCanEscrow, 0x00000008},
		{"LsmfMPTCanMutateCanTrade", LsmfMPTCanMutateCanTrade, 0x00000010},
		{"LsmfMPTCanMutateCanTransfer", LsmfMPTCanMutateCanTransfer, 0x00000020},
		{"LsmfMPTCanMutateCanClawback", LsmfMPTCanMutateCanClawback, 0x00000040},
		{"LsmfMPTCanMutateMetadata", LsmfMPTCanMutateMetadata, 0x00010000},
		{"LsmfMPTCanMutateTransferFee", LsmfMPTCanMutateTransferFee, 0x00020000},

		// ltMPTOKEN
		{"LsfMPTAuthorized", LsfMPTAuthorized, 0x00000002},
		{"LsfMPTAMM", LsfMPTAMM, 0x00000004},

		// ltCREDENTIAL
		{"LsfAccepted", LsfAccepted, 0x00010000},

		// ltVAULT
		{"LsfVaultPrivate", LsfVaultPrivate, 0x00010000},

		// ltLOAN
		{"LsfLoanDefault", LsfLoanDefault, 0x00010000},
		{"LsfLoanImpaired", LsfLoanImpaired, 0x00020000},
		{"LsfLoanOverpayment", LsfLoanOverpayment, 0x00040000},
	}

	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = 0x%08X, want 0x%08X", tt.name, tt.got, tt.want)
		}
	}

	oracle := readOracleLedgerFlags(t)
	if len(oracle) != len(tests) {
		t.Fatalf("rippled has %d ledger flags, Go has %d", len(oracle), len(tests))
	}
	for _, tt := range tests {
		oracleName := strings.ToLower(tt.name[:1]) + tt.name[1:]
		want, ok := oracle[oracleName]
		if !ok {
			t.Errorf("%s is absent from rippled LedgerFormats.h", tt.name)
			continue
		}
		if tt.got != want {
			t.Errorf("%s = 0x%08X, rippled = 0x%08X", tt.name, tt.got, want)
		}
	}
}

var oracleLedgerFlag = regexp.MustCompile(`LSF_FLAG2?\((ls(?:m)?f\w+),\s*(0x[0-9a-fA-F]+)\)`)

func readOracleLedgerFlags(t *testing.T) map[string]uint32 {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve flags test source path")
	}
	dir := filepath.Dir(file)
	for range 12 {
		path := filepath.Join(dir, "rippled-worktrees", "v3.2.0-oracle", "include", "xrpl", "protocol", "LedgerFormats.h")
		data, err := os.ReadFile(path)
		if err == nil {
			flags := make(map[string]uint32)
			for _, match := range oracleLedgerFlag.FindAllStringSubmatch(string(data), -1) {
				value, err := strconv.ParseUint(match[2], 0, 32)
				if err != nil {
					t.Fatalf("parse %s: %v", match[0], err)
				}
				flags[match[1]] = uint32(value)
			}
			return flags
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("required rippled v3.2.0 LedgerFormats.h not found from %s", file)
	return nil
}

func TestMPTokenProtocolLimits(t *testing.T) {
	if MaxMPTokenMetadataLength != 1024 {
		t.Errorf("MaxMPTokenMetadataLength = %d, want 1024", MaxMPTokenMetadataLength)
	}
	if MaxTransferFee != 50_000 {
		t.Errorf("MaxTransferFee = %d, want 50000", MaxTransferFee)
	}
	if MaxMPTokenAmount != 0x7FFF_FFFF_FFFF_FFFF {
		t.Errorf("MaxMPTokenAmount = %d, want 2^63-1", MaxMPTokenAmount)
	}
}
