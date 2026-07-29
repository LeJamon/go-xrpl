package sponsor

import "github.com/LeJamon/go-xrpl/internal/tx"

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

const (
	SponsorshipTransferFlagEnd      uint32 = 0x00010000
	SponsorshipTransferFlagCreate   uint32 = 0x00020000
	SponsorshipTransferFlagReassign uint32 = 0x00040000
)

const sponsorshipTransferValidFlags = tx.TfUniversal |
	SponsorshipTransferFlagEnd |
	SponsorshipTransferFlagCreate |
	SponsorshipTransferFlagReassign
