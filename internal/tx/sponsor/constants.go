// Package sponsor implements the XLS-68 SponsorshipSet and
// SponsorshipTransfer lifecycle transactions.
package sponsor

import "github.com/LeJamon/go-xrpl/internal/tx"

// SponsorshipSet transaction flags.
const (
	SponsorshipSetFlagRequireSignForFee          uint32 = 0x00010000
	SponsorshipSetFlagClearRequireSignForFee     uint32 = 0x00020000
	SponsorshipSetFlagRequireSignForReserve      uint32 = 0x00040000
	SponsorshipSetFlagClearRequireSignForReserve uint32 = 0x00080000
	SponsorshipSetFlagDelete                     uint32 = 0x00100000
)

const sponsorshipSetValidFlags = tx.TfUniversal |
	SponsorshipSetFlagRequireSignForFee |
	SponsorshipSetFlagClearRequireSignForFee |
	SponsorshipSetFlagRequireSignForReserve |
	SponsorshipSetFlagClearRequireSignForReserve |
	SponsorshipSetFlagDelete

// SponsorshipTransfer transaction flags. Exactly one must be present.
const (
	SponsorshipTransferFlagEnd      uint32 = 0x00010000
	SponsorshipTransferFlagCreate   uint32 = 0x00020000
	SponsorshipTransferFlagReassign uint32 = 0x00040000
)

const sponsorshipTransferValidFlags = tx.TfUniversal |
	SponsorshipTransferFlagEnd |
	SponsorshipTransferFlagCreate |
	SponsorshipTransferFlagReassign
