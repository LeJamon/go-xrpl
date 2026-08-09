package protocol

import "fmt"

// LedgerEntryType is an XRPL ledger-entry type code (the lt* enumeration).
type LedgerEntryType uint16

// Ledger-entry type sentinel codes.
const (
	LedgerEntryTypeAny   LedgerEntryType = 0
	LedgerEntryTypeChild LedgerEntryType = 0x1CD2

	LedgerEntryTypeNFTokenOffer                    LedgerEntryType = 0x0037
	LedgerEntryTypeCheck                           LedgerEntryType = 0x0043
	LedgerEntryTypeDID                             LedgerEntryType = 0x0049
	LedgerEntryTypeNegativeUNL                     LedgerEntryType = 0x004E
	LedgerEntryTypeNFTokenPage                     LedgerEntryType = 0x0050
	LedgerEntryTypeSignerList                      LedgerEntryType = 0x0053
	LedgerEntryTypeTicket                          LedgerEntryType = 0x0054
	LedgerEntryTypeAccountRoot                     LedgerEntryType = 0x0061
	LedgerEntryTypeContract                        LedgerEntryType = 0x0063
	LedgerEntryTypeDirectoryNode                   LedgerEntryType = 0x0064
	LedgerEntryTypeAmendments                      LedgerEntryType = 0x0066
	LedgerEntryTypeGeneratorMap                    LedgerEntryType = 0x0067
	LedgerEntryTypeLedgerHashes                    LedgerEntryType = 0x0068
	LedgerEntryTypeBridge                          LedgerEntryType = 0x0069
	LedgerEntryTypeNickname                        LedgerEntryType = 0x006E
	LedgerEntryTypeOffer                           LedgerEntryType = 0x006F
	LedgerEntryTypeDepositPreauth                  LedgerEntryType = 0x0070
	LedgerEntryTypeXChainOwnedClaimID              LedgerEntryType = 0x0071
	LedgerEntryTypeRippleState                     LedgerEntryType = 0x0072
	LedgerEntryTypeFeeSettings                     LedgerEntryType = 0x0073
	LedgerEntryTypeXChainOwnedCreateAccountClaimID LedgerEntryType = 0x0074
	LedgerEntryTypeEscrow                          LedgerEntryType = 0x0075
	LedgerEntryTypePayChannel                      LedgerEntryType = 0x0078
	LedgerEntryTypeAMM                             LedgerEntryType = 0x0079
	LedgerEntryTypeMPTokenIssuance                 LedgerEntryType = 0x007E
	LedgerEntryTypeMPToken                         LedgerEntryType = 0x007F
	LedgerEntryTypeOracle                          LedgerEntryType = 0x0080
	LedgerEntryTypeCredential                      LedgerEntryType = 0x0081
	LedgerEntryTypePermissionedDomain              LedgerEntryType = 0x0082
	LedgerEntryTypeDelegate                        LedgerEntryType = 0x0083
	LedgerEntryTypeVault                           LedgerEntryType = 0x0084
	LedgerEntryTypeLoanBroker                      LedgerEntryType = 0x0088
	LedgerEntryTypeLoan                            LedgerEntryType = 0x0089
	LedgerEntryTypeSponsorship                     LedgerEntryType = 0x0090
)

// LedgerEntryTypeInfo describes one concrete ledger-entry type.
type LedgerEntryTypeInfo struct {
	Type       LedgerEntryType
	Name       string
	RPCName    string
	Deprecated bool
}

var ledgerEntryTypes = [...]LedgerEntryTypeInfo{
	{LedgerEntryTypeNFTokenOffer, "NFTokenOffer", "nft_offer", false},
	{LedgerEntryTypeCheck, "Check", "check", false},
	{LedgerEntryTypeDID, "DID", "did", false},
	{LedgerEntryTypeNegativeUNL, "NegativeUNL", "nunl", false},
	{LedgerEntryTypeNFTokenPage, "NFTokenPage", "nft_page", false},
	{LedgerEntryTypeSignerList, "SignerList", "signer_list", false},
	{LedgerEntryTypeTicket, "Ticket", "ticket", false},
	{LedgerEntryTypeAccountRoot, "AccountRoot", "account", false},
	{LedgerEntryTypeContract, "Contract", "", true},
	{LedgerEntryTypeDirectoryNode, "DirectoryNode", "directory", false},
	{LedgerEntryTypeAmendments, "Amendments", "amendments", false},
	{LedgerEntryTypeGeneratorMap, "GeneratorMap", "", true},
	{LedgerEntryTypeLedgerHashes, "LedgerHashes", "hashes", false},
	{LedgerEntryTypeBridge, "Bridge", "bridge", false},
	{LedgerEntryTypeNickname, "Nickname", "", true},
	{LedgerEntryTypeOffer, "Offer", "offer", false},
	{LedgerEntryTypeDepositPreauth, "DepositPreauth", "deposit_preauth", false},
	{LedgerEntryTypeXChainOwnedClaimID, "XChainOwnedClaimID", "xchain_owned_claim_id", false},
	{LedgerEntryTypeRippleState, "RippleState", "state", false},
	{LedgerEntryTypeFeeSettings, "FeeSettings", "fee", false},
	{LedgerEntryTypeXChainOwnedCreateAccountClaimID, "XChainOwnedCreateAccountClaimID", "xchain_owned_create_account_claim_id", false},
	{LedgerEntryTypeEscrow, "Escrow", "escrow", false},
	{LedgerEntryTypePayChannel, "PayChannel", "payment_channel", false},
	{LedgerEntryTypeAMM, "AMM", "amm", false},
	{LedgerEntryTypeMPTokenIssuance, "MPTokenIssuance", "mpt_issuance", false},
	{LedgerEntryTypeMPToken, "MPToken", "mptoken", false},
	{LedgerEntryTypeOracle, "Oracle", "oracle", false},
	{LedgerEntryTypeCredential, "Credential", "credential", false},
	{LedgerEntryTypePermissionedDomain, "PermissionedDomain", "permissioned_domain", false},
	{LedgerEntryTypeDelegate, "Delegate", "delegate", false},
	{LedgerEntryTypeVault, "Vault", "vault", false},
	{LedgerEntryTypeLoanBroker, "LoanBroker", "loan_broker", false},
	{LedgerEntryTypeLoan, "Loan", "loan", false},
	{LedgerEntryTypeSponsorship, "Sponsorship", "sponsorship", false},
}

// LedgerEntryTypes returns every concrete ledger-entry type in wire-code order.
func LedgerEntryTypes() []LedgerEntryTypeInfo {
	out := make([]LedgerEntryTypeInfo, len(ledgerEntryTypes))
	copy(out, ledgerEntryTypes[:])
	return out
}

// LedgerEntryTypeByCode resolves a concrete ledger-entry type code.
func LedgerEntryTypeByCode(code LedgerEntryType) (LedgerEntryTypeInfo, bool) {
	for _, info := range ledgerEntryTypes {
		if info.Type == code {
			return info, true
		}
	}
	return LedgerEntryTypeInfo{}, false
}

// LedgerEntryTypeByName resolves a canonical ledger-entry type name.
func LedgerEntryTypeByName(name string) (LedgerEntryTypeInfo, bool) {
	for _, info := range ledgerEntryTypes {
		if info.Name == name {
			return info, true
		}
	}
	return LedgerEntryTypeInfo{}, false
}

// LedgerEntryTypeByRPCName resolves a ledger_data type selector.
func LedgerEntryTypeByRPCName(name string) (LedgerEntryTypeInfo, bool) {
	for _, info := range ledgerEntryTypes {
		if info.RPCName != "" && info.RPCName == name {
			return info, true
		}
	}
	return LedgerEntryTypeInfo{}, false
}

func (t LedgerEntryType) String() string {
	if info, ok := LedgerEntryTypeByCode(t); ok {
		return info.Name
	}
	return fmt.Sprintf("Unknown(0x%04x)", uint16(t))
}
