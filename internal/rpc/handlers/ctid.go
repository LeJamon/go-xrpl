package handlers

import (
	"fmt"
	"strconv"
)

const (
	ctidMaxLedgerSeq = uint32(0x0FFFFFFF)
	ctidMaxComponent = uint32(0xFFFF)
	ctidPrefix       = uint64(0xC000000000000000)
	ctidPrefixMask   = uint64(0xF000000000000000)
)

func encodeCTID(ledgerSeq, txIndex, networkID uint32) (string, bool) {
	if ledgerSeq > ctidMaxLedgerSeq || txIndex > ctidMaxComponent || networkID > ctidMaxComponent {
		return "", false
	}
	value := ctidPrefix | uint64(ledgerSeq)<<32 | uint64(txIndex)<<16 | uint64(networkID)
	return fmt.Sprintf("%016X", value), true
}

func encodeTxResponseCTID(ledgerSeq, txIndex, networkID uint32) (string, bool) {
	if ledgerSeq >= ctidMaxLedgerSeq || txIndex > ctidMaxComponent || networkID >= ctidMaxComponent {
		return "", false
	}
	return encodeCTID(ledgerSeq, txIndex, networkID)
}

func transactionNetworkID(txJSON map[string]any, fallback uint32) uint32 {
	if networkID, ok := jsonUint32(txJSON["NetworkID"]); ok {
		return networkID
	}
	return fallback
}

func parseCTID(ctid string) (ledgerSeq uint32, txIndex uint16, networkID uint16, err error) {
	if len(ctid) != 16 {
		return 0, 0, 0, fmt.Errorf("CTID must be 16 hex characters")
	}
	value, err := strconv.ParseUint(ctid, 16, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid CTID hex")
	}
	if value&ctidPrefixMask != ctidPrefix {
		return 0, 0, 0, fmt.Errorf("invalid CTID marker")
	}
	return uint32(value>>32) & ctidMaxLedgerSeq, uint16(value >> 16), uint16(value), nil
}
