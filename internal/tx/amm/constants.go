package amm

import (
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// AMM constants matching rippled
const (
	// TRADING_FEE_THRESHOLD is the maximum trading fee (1000 = 1%)
	TRADING_FEE_THRESHOLD uint16 = 1000

	// AMM vote slot constants
	VOTE_MAX_SLOTS           = 8
	VOTE_WEIGHT_SCALE_FACTOR = 100000

	// AMM auction slot constants
	AUCTION_SLOT_MAX_AUTH_ACCOUNTS       = 4
	AUCTION_SLOT_TIME_INTERVALS          = 20
	AUCTION_SLOT_DISCOUNTED_FEE_FRACTION = 10           // 1/10 of fee
	AUCTION_SLOT_MIN_FEE_FRACTION        = 25           // 1/25 of fee
	TOTAL_TIME_SLOT_SECS                 = 24 * 60 * 60 // 24 hours
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

// Internal constants (lowercase aliases of exported AMM constants)
const (
	voteMaxSlots          = VOTE_MAX_SLOTS
	voteWeightScaleFactor = VOTE_WEIGHT_SCALE_FACTOR

	auctionSlotDiscountedFee    = AUCTION_SLOT_DISCOUNTED_FEE_FRACTION
	auctionSlotMinFeeFraction   = AUCTION_SLOT_MIN_FEE_FRACTION
	auctionSlotTimeIntervals    = AUCTION_SLOT_TIME_INTERVALS
	auctionSlotTotalTimeSecs    = uint32(TOTAL_TIME_SLOT_SECS)
	auctionSlotIntervalDuration = auctionSlotTotalTimeSecs / auctionSlotTimeIntervals
)

// Result code aliases for AMM-specific codes
var (
	TecUNFUNDED_AMM       = tx.TecUNFUNDED_AMM
	TecNO_LINE            = tx.TecNO_LINE
	TecINSUF_RESERVE_LINE = tx.TecINSUF_RESERVE_LINE
	TerNO_AMM             = tx.TerNO_AMM
	TerNO_ACCOUNT         = tx.TerNO_ACCOUNT
)
