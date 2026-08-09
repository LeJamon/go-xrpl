package entry

import "github.com/LeJamon/go-xrpl/protocol"

// LedgerSpecificFlags mirrors rippled's LedgerSpecificFlags enum.
// Reference: rippled/include/xrpl/protocol/LedgerFormats.h (LedgerSpecificFlags).
// Values are scoped per ledger entry type; identical numeric values are
// reused across types and disambiguated by name.
const (
	// ltACCOUNT_ROOT
	LsfPasswordSpent                uint32 = 0x00010000
	LsfRequireDestTag               uint32 = 0x00020000
	LsfRequireAuth                  uint32 = 0x00040000
	LsfDisallowXRP                  uint32 = 0x00080000
	LsfDisableMaster                uint32 = 0x00100000
	LsfNoFreeze                     uint32 = 0x00200000
	LsfGlobalFreeze                 uint32 = 0x00400000
	LsfDefaultRipple                uint32 = 0x00800000
	LsfDepositAuth                  uint32 = 0x01000000
	LsfDisallowIncomingNFTokenOffer uint32 = 0x04000000
	LsfDisallowIncomingCheck        uint32 = 0x08000000
	LsfDisallowIncomingPayChan      uint32 = 0x10000000
	LsfDisallowIncomingTrustline    uint32 = 0x20000000
	LsfAllowTrustLineLocking        uint32 = 0x40000000
	LsfAllowTrustLineClawback       uint32 = 0x80000000

	// ltOFFER
	LsfPassive uint32 = 0x00010000
	LsfSell    uint32 = 0x00020000
	LsfHybrid  uint32 = 0x00040000

	// ltRIPPLE_STATE
	LsfLowReserve     uint32 = 0x00010000
	LsfHighReserve    uint32 = 0x00020000
	LsfLowAuth        uint32 = 0x00040000
	LsfHighAuth       uint32 = 0x00080000
	LsfLowNoRipple    uint32 = 0x00100000
	LsfHighNoRipple   uint32 = 0x00200000
	LsfLowFreeze      uint32 = 0x00400000
	LsfHighFreeze     uint32 = 0x00800000
	LsfAMMNode        uint32 = 0x01000000
	LsfLowDeepFreeze  uint32 = 0x02000000
	LsfHighDeepFreeze uint32 = 0x04000000

	// ltSIGNER_LIST
	LsfOneOwnerCount uint32 = 0x00010000

	// ltDIR_NODE
	LsfNFTokenBuyOffers  uint32 = 0x00000001
	LsfNFTokenSellOffers uint32 = 0x00000002

	// ltNFTOKEN_OFFER
	LsfSellNFToken uint32 = 0x00000001

	// ltMPTOKEN_ISSUANCE (also LsfMPTLocked applies to ltMPTOKEN)
	LsfMPTLocked                     uint32 = 0x00000001
	LsfMPTCanLock                    uint32 = 0x00000002
	LsfMPTRequireAuth                uint32 = 0x00000004
	LsfMPTCanEscrow                  uint32 = 0x00000008
	LsfMPTCanTrade                   uint32 = 0x00000010
	LsfMPTCanTransfer                uint32 = 0x00000020
	LsfMPTCanClawback                uint32 = 0x00000040
	LsfMPTCanHoldConfidentialBalance uint32 = 0x00000080

	// ltMPTOKEN_ISSUANCE sfImmutableFlags
	LsifMPTCanLock                    uint32 = 0x00000002
	LsifMPTRequireAuth                uint32 = 0x00000004
	LsifMPTCanEscrow                  uint32 = 0x00000008
	LsifMPTCanTrade                   uint32 = 0x00000010
	LsifMPTCanTransfer                uint32 = 0x00000020
	LsifMPTCanClawback                uint32 = 0x00000040
	LsifMPTCanHoldConfidentialBalance uint32 = 0x00000080
	LsifMPTMetadata                   uint32 = 0x00010000
	LsifMPTTransferFee                uint32 = 0x00020000

	// ltMPTOKEN
	LsfMPTAuthorized uint32 = 0x00000002
	LsfMPTAMM        uint32 = 0x00000004

	// ltCREDENTIAL
	LsfAccepted uint32 = 0x00010000

	// ltVAULT
	LsfVaultPrivate uint32 = 0x00010000

	// ltLOAN
	LsfLoanDefault     uint32 = 0x00010000
	LsfLoanImpaired    uint32 = 0x00020000
	LsfLoanOverpayment uint32 = 0x00040000

	LsfSponsorshipRequireSignForFee     uint32 = 0x00010000
	LsfSponsorshipRequireSignForReserve uint32 = 0x00020000
)

// Deprecated MPToken protocol-limit aliases.
const (
	// Deprecated: use protocol.MaxMPTokenMetadataLength.
	MaxMPTokenMetadataLength = protocol.MaxMPTokenMetadataLength
	// Deprecated: use protocol.MaxMPTokenTransferFee.
	MaxTransferFee = protocol.MaxMPTokenTransferFee
	// Deprecated: use protocol.MaxMPTokenAmount.
	MaxMPTokenAmount = protocol.MaxMPTokenAmount
)
