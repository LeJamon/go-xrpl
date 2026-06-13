package amm

import (
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// AMM constants matching rippled.
const (
	// tradingFeeThreshold is the maximum trading fee (1000 = 1%).
	tradingFeeThreshold uint16 = 1000

	// Vote slot constants.
	voteMaxSlots          = 8
	voteWeightScaleFactor = 100000

	// Auction slot constants.
	auctionSlotMaxAuthAccounts       = 4
	auctionSlotTimeIntervals         = 20
	auctionSlotDiscountedFeeFraction = 10           // 1/10 of fee
	auctionSlotMinFeeFraction        = 25           // 1/25 of fee
	totalTimeSlotSecs                = 24 * 60 * 60 // 24 hours

	auctionSlotTotalTimeSecs    = uint32(totalTimeSlotSecs)
	auctionSlotIntervalDuration = auctionSlotTotalTimeSecs / auctionSlotTimeIntervals
)

// Transaction flags

const (
	// AMM* masks mirror rippled: any non-universal bit is rejected, allowing
	// universal flags (tfFullyCanonicalSig, tfInnerBatchTxn) through. References:
	// rippled AMMCreate.cpp:43, AMMVote.cpp:46, AMMBid.cpp:42, AMMDelete.cpp:39
	// (each `getFlags() & tfUniversalMask`); TxFlags.h:222-227 for the *Mask
	// constants used here.

	// AMMCreate has no valid transaction flags beyond universal.
	tfAMMCreateMask uint32 = tx.TfUniversalMask

	// AMMDeposit flags
	tfLPToken         uint32 = 0x00010000
	tfSingleAsset     uint32 = 0x00080000
	tfTwoAsset        uint32 = 0x00100000
	tfOneAssetLPToken uint32 = 0x00200000
	tfLimitLPToken    uint32 = 0x00400000
	tfTwoAssetIfEmpty uint32 = 0x00800000
	// tfDepositSubTx is the combination of all deposit mode flags (used for popcount check)
	tfDepositSubTx   uint32 = tfLPToken | tfSingleAsset | tfTwoAsset | tfOneAssetLPToken | tfLimitLPToken | tfTwoAssetIfEmpty
	tfAMMDepositMask uint32 = ^(tx.TfUniversal | tfDepositSubTx)

	// AMMWithdraw flags
	tfWithdrawAll         uint32 = 0x00020000
	tfOneAssetWithdrawAll uint32 = 0x00040000
	tfAMMWithdrawMask     uint32 = ^(tx.TfUniversal | tfLPToken | tfWithdrawAll | tfOneAssetWithdrawAll | tfSingleAsset | tfTwoAsset | tfOneAssetLPToken | tfLimitLPToken)

	// AMMVote has no valid transaction flags beyond universal.
	tfAMMVoteMask uint32 = tx.TfUniversalMask

	// AMMBid has no valid transaction flags beyond universal.
	tfAMMBidMask uint32 = tx.TfUniversalMask

	// AMMDelete has no valid transaction flags beyond universal.
	tfAMMDeleteMask uint32 = tx.TfUniversalMask

	// AMMClawback flags
	tfClawTwoAssets   uint32 = 0x00000001
	tfAMMClawbackMask uint32 = ^(tx.TfUniversal | tfClawTwoAssets)
)

// Result code aliases for AMM-specific codes
var (
	TecUNFUNDED_AMM       = tx.TecUNFUNDED_AMM
	TecNO_LINE            = tx.TecNO_LINE
	TecINSUF_RESERVE_LINE = tx.TecINSUF_RESERVE_LINE
	TerNO_AMM             = tx.TerNO_AMM
	TerNO_ACCOUNT         = tx.TerNO_ACCOUNT
)
