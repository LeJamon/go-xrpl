package entry

import (
	"fmt"
	"testing"
)

// ledgerTypes is the golden table of every ledger entry type code, pinned
// against rippled. A wrong type code corrupts ledger-object serialization and
// keylet derivation network-wide and is otherwise invisible until a hash
// divergence in production.
// Reference: rippled/include/xrpl/protocol/detail/ledger_entries.macro
var ledgerTypes = []struct {
	typ  Type
	code uint16
	name string
}{
	{TypeNFTokenOffer, 0x0037, "NFTokenOffer"},
	{TypeCheck, 0x0043, "Check"},
	{TypeDID, 0x0049, "DID"},
	{TypeNegativeUNL, 0x004e, "NegativeUNL"},
	{TypeNFTokenPage, 0x0050, "NFTokenPage"},
	{TypeSignerList, 0x0053, "SignerList"},
	{TypeTicket, 0x0054, "Ticket"},
	{TypeAccountRoot, 0x0061, "AccountRoot"},
	{TypeDirectoryNode, 0x0064, "DirectoryNode"},
	{TypeAmendments, 0x0066, "Amendments"},
	{TypeLedgerHashes, 0x0068, "LedgerHashes"},
	{TypeBridge, 0x0069, "Bridge"},
	{TypeOffer, 0x006f, "Offer"},
	{TypeDepositPreauth, 0x0070, "DepositPreauth"},
	{TypeXChainOwnedClaimID, 0x0071, "XChainOwnedClaimID"},
	{TypeRippleState, 0x0072, "RippleState"},
	{TypeFeeSettings, 0x0073, "FeeSettings"},
	{TypeXChainOwnedCreateAccountClaimID, 0x0074, "XChainOwnedCreateAccountClaimID"},
	{TypeEscrow, 0x0075, "Escrow"},
	{TypePayChannel, 0x0078, "PayChannel"},
	{TypeAMM, 0x0079, "AMM"},
	{TypeMPTokenIssuance, 0x007e, "MPTokenIssuance"},
	{TypeMPToken, 0x007f, "MPToken"},
	{TypeOracle, 0x0080, "Oracle"},
	{TypeCredential, 0x0081, "Credential"},
	{TypePermissionedDomain, 0x0082, "PermissionedDomain"},
	{TypeDelegate, 0x0083, "Delegate"},
	{TypeVault, 0x0084, "Vault"},
}

func TestTypeCodes(t *testing.T) {
	if got, want := len(ledgerTypes), 28; got != want {
		t.Fatalf("ledgerTypes covers %d types, want %d — update the golden table when ledger_entries.macro changes", got, want)
	}

	seen := make(map[uint16]string, len(ledgerTypes))
	for _, tc := range ledgerTypes {
		if uint16(tc.typ) != tc.code {
			t.Errorf("%s = 0x%04X, want 0x%04X", tc.name, uint16(tc.typ), tc.code)
		}
		if prev, ok := seen[tc.code]; ok {
			t.Errorf("type code 0x%04X is shared by %s and %s", tc.code, prev, tc.name)
		}
		seen[tc.code] = tc.name
	}
}

func TestTypeString(t *testing.T) {
	for _, tc := range ledgerTypes {
		if got := tc.typ.String(); got != tc.name {
			t.Errorf("Type(0x%04X).String() = %q, want %q", uint16(tc.typ), got, tc.name)
		}
	}
}

func TestTypeStringUnknown(t *testing.T) {
	for _, code := range []uint16{0x0000, 0x0001, 0x00ff, 0xffff} {
		want := fmt.Sprintf("Unknown(%#x)", code)
		if got := Type(code).String(); got != want {
			t.Errorf("Type(0x%04X).String() = %q, want %q", code, got, want)
		}
	}
}
