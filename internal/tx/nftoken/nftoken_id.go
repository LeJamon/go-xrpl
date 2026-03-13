package nftoken

import (
	"bytes"
	"encoding/binary"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

// NFToken ID flag constants (stored in first 2 bytes of NFTokenID).
// These match the mint flags but are used when constructing/inspecting NFToken IDs.
const (
	NFTokenFlagBurnable     uint16 = 0x0001
	NFTokenFlagOnlyXRP      uint16 = 0x0002
	NFTokenFlagTrustLine    uint16 = 0x0004
	NFTokenFlagTransferable uint16 = 0x0008
	NFTokenFlagMutable      uint16 = 0x0010
)

// nftPageMask is the mask for the low 96 bits of an NFTokenID
// This is used to group equivalent NFTs on the same page
var nftPageMask = [32]byte{
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0xFF, 0xFF, 0xFF, 0xFF,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
}

// getNFTIssuer extracts the issuer AccountID from an NFTokenID
// NFTokenID format: Flags(2) + TransferFee(2) + Issuer(20) + Taxon(4) + Sequence(4)
func getNFTIssuer(nftokenID [32]byte) [20]byte {
	var issuer [20]byte
	copy(issuer[:], nftokenID[4:24])
	return issuer
}

// getNFTokenFlags extracts the flags from an NFTokenID string (first 4 hex chars)
func getNFTokenFlags(nftokenID string) uint16 {
	if len(nftokenID) < 4 {
		return 0
	}
	var flags uint16
	for i := 0; i < 4 && i < len(nftokenID); i++ {
		flags <<= 4
		c := nftokenID[i]
		switch {
		case c >= '0' && c <= '9':
			flags |= uint16(c - '0')
		case c >= 'a' && c <= 'f':
			flags |= uint16(c - 'a' + 10)
		case c >= 'A' && c <= 'F':
			flags |= uint16(c - 'A' + 10)
		}
	}
	return flags
}

// getNFTTransferFee extracts the transfer fee from an NFTokenID
func getNFTTransferFee(nftokenID [32]byte) uint16 {
	return binary.BigEndian.Uint16(nftokenID[2:4])
}

// getNFTFlagsFromID extracts the flags from an NFTokenID
func getNFTFlagsFromID(nftokenID [32]byte) uint16 {
	return binary.BigEndian.Uint16(nftokenID[0:2])
}

// CipheredTaxon ciphers a taxon using rippled's algorithm to prevent enumeration.
// Matching rippled: (taxon ^ ((tokenSeq ^ 384160001) * 2357503715))
func CipheredTaxon(tokenSeq uint32, taxon uint32) uint32 {
	return cipheredTaxon(tokenSeq, taxon)
}

func cipheredTaxon(tokenSeq uint32, taxon uint32) uint32 {
	return taxon ^ ((tokenSeq ^ 384160001) * 2357503715)
}

// GenerateNFTokenID generates an NFTokenID based on the minting parameters.
// This is the exported version of generateNFTokenID for use in tests.
// Reference: rippled NFTokenMint.cpp createNFTokenID
func GenerateNFTokenID(issuer [20]byte, taxon, sequence uint32, flags uint16, transferFee uint16) [32]byte {
	return generateNFTokenID(issuer, taxon, sequence, flags, transferFee)
}

// generateNFTokenID generates an NFTokenID based on the minting parameters
// Reference: rippled NFTokenMint.cpp createNFTokenID
func generateNFTokenID(issuer [20]byte, taxon, sequence uint32, flags uint16, transferFee uint16) [32]byte {
	var tokenID [32]byte

	// NFTokenID format (32 bytes):
	// Bytes 0-1: Flags (2 bytes, big endian)
	// Bytes 2-3: TransferFee (2 bytes, big endian)
	// Bytes 4-23: Issuer AccountID (20 bytes)
	// Bytes 24-27: Taxon (ciphered, 4 bytes, big endian)
	// Bytes 28-31: Sequence (4 bytes, big endian)

	binary.BigEndian.PutUint16(tokenID[0:2], flags)
	binary.BigEndian.PutUint16(tokenID[2:4], transferFee)
	copy(tokenID[4:24], issuer[:])

	ciphered := cipheredTaxon(sequence, taxon)
	binary.BigEndian.PutUint32(tokenID[24:28], ciphered)
	binary.BigEndian.PutUint32(tokenID[28:32], sequence)

	return tokenID
}

// ---------------------------------------------------------------------------
// NFToken comparison and page key helpers
// ---------------------------------------------------------------------------

// compareNFTokenID compares two NFTokenIDs using rippled's sort order:
// sort by low 96 bits first, then full 256-bit ID as tiebreaker.
// Reference: rippled NFTokenUtils.cpp compareTokens
func compareNFTokenID(a, b [32]byte) int {
	// Compare low 96 bits (bytes 20-31) first
	if c := bytes.Compare(a[20:], b[20:]); c != 0 {
		return c
	}
	// Full 256-bit comparison as tiebreaker
	return bytes.Compare(a[:], b[:])
}

// getNFTPageKey returns the low 96 bits of an NFTokenID (for page grouping)
func getNFTPageKey(nftokenID [32]byte) [32]byte {
	var result [32]byte
	for i := 0; i < 32; i++ {
		result[i] = nftokenID[i] & nftPageMask[i]
	}
	return result
}

// insertNFTokenSorted inserts an NFToken into the slice maintaining sorted order
func insertNFTokenSorted(tokens []state.NFTokenData, newToken state.NFTokenData) []state.NFTokenData {
	pos := 0
	for i, t := range tokens {
		if compareNFTokenID(newToken.NFTokenID, t.NFTokenID) < 0 {
			pos = i
			break
		}
		pos = i + 1
	}
	tokens = append(tokens, state.NFTokenData{})
	copy(tokens[pos+1:], tokens[pos:])
	tokens[pos] = newToken
	return tokens
}

// uint256Next returns id + 1 (for page key derivation during splits)
func uint256Next(id [32]byte) [32]byte {
	result := id
	for i := 31; i >= 0; i-- {
		result[i]++
		if result[i] != 0 {
			break
		}
	}
	return result
}
