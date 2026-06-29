package state

import (
	"encoding/hex"
	"fmt"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
)

// OracleData holds parsed fields of an Oracle ledger entry.
// Reference: rippled LedgerFormats.h ltORACLE
type OracleData struct {
	Owner           [20]byte
	Provider        string // hex-encoded
	AssetClass      string // hex-encoded
	LastUpdateTime  uint32
	OwnerNode       uint64
	PriceDataSeries []OraclePriceData
	URI             string // hex-encoded, optional
	Flags           uint32
	// PreviousTxnID / PreviousTxnLgrSeq thread the Oracle SLE's modification
	// history. They must round-trip so a no-op OracleSet (re-submitting the
	// current price data) re-serializes byte-identically, letting the apply
	// layer's unchanged-entry guard prune it — matching rippled, which emits no
	// ModifiedNode and threads no PreviousTxnID when nothing changed
	// (ApplyStateTable.cpp:154-157). Zero when the Oracle has never been threaded;
	// omitted on serialize in that case.
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32
}

// OraclePriceData holds parsed fields of a single price data entry within an Oracle.
type OraclePriceData struct {
	BaseAsset  string // 3-letter currency code or hex
	QuoteAsset string // 3-letter currency code or hex
	AssetPrice uint64
	Scale      uint8
	HasPrice   bool
	HasScale   bool
}

// Field nth values for Oracle fields
const (
	fieldLastUpdateTime = 15 // UInt32, nth=15
	fieldOwnerNode      = 4  // UInt64, nth=4
	fieldAssetPrice     = 23 // UInt64, nth=23
	fieldScale          = 4  // UInt8, nth=4
	fieldOwner          = 2  // AccountID, nth=2
	fieldProvider       = 29 // Blob, nth=29
	fieldAssetClass     = 28 // Blob, nth=28
	fieldURI            = 5  // Blob, nth=5
	fieldBaseAsset      = 1  // Currency, nth=1
	fieldQuoteAsset     = 2  // Currency, nth=2
	fieldPriceDataSer   = 24 // STArray, nth=24
)

// ParseOracle parses an Oracle ledger entry from binary data.
func ParseOracle(data []byte) (*OracleData, error) {
	oracle := &OracleData{}

	err := WalkFields(data, func(f Field) error {
		switch f.TypeCode {
		case stUInt32:
			switch f.FieldCode {
			case 2: // Flags
				oracle.Flags = f.UInt32()
			case 5: // PreviousTxnLgrSeq
				oracle.PreviousTxnLgrSeq = f.UInt32()
			case fieldLastUpdateTime: // 15
				oracle.LastUpdateTime = f.UInt32()
			}

		case stUInt64:
			if f.FieldCode == fieldOwnerNode { // 4
				oracle.OwnerNode = f.UInt64()
			}

		case stHash256:
			if f.FieldCode == 5 { // PreviousTxnID
				oracle.PreviousTxnID = f.Hash256()
			}

		case stAccountID:
			if id, ok := f.AccountID(); ok && f.FieldCode == fieldOwner { // 2
				oracle.Owner = id
			}

		case stBlob:
			blobHex := hex.EncodeToString(f.VLBytes())
			switch f.FieldCode {
			case fieldProvider: // 29
				oracle.Provider = blobHex
			case fieldAssetClass: // 28
				oracle.AssetClass = blobHex
			case fieldURI: // 5
				oracle.URI = blobHex
			}

		case stArray:
			if f.FieldCode == fieldPriceDataSer { // 24
				series, err := parseOraclePriceDataSeries(f.Value)
				if err != nil {
					return err
				}
				oracle.PriceDataSeries = series
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return oracle, nil
}

// parseOraclePriceDataSeries decodes the PriceDataSeries STArray content (as
// delimited by WalkFields). Each element is a PriceData STObject.
func parseOraclePriceDataSeries(content []byte) ([]OraclePriceData, error) {
	var series []OraclePriceData
	err := WalkFields(content, func(elem Field) error {
		if elem.TypeCode != stObject {
			return nil
		}
		pd := OraclePriceData{}
		if err := WalkFields(elem.Value, func(inner Field) error {
			switch inner.TypeCode {
			case stUInt64:
				if inner.FieldCode == fieldAssetPrice { // 23
					pd.AssetPrice = inner.UInt64()
					pd.HasPrice = true
				}
			case stUInt8:
				if inner.FieldCode == fieldScale { // 4
					pd.Scale = inner.UInt8()
					pd.HasScale = true
				}
			case stCurrency:
				currStr := parseCurrencyBytes(inner.Value)
				switch inner.FieldCode {
				case fieldBaseAsset: // 1
					pd.BaseAsset = currStr
				case fieldQuoteAsset: // 2
					pd.QuoteAsset = currStr
				}
			}
			return nil
		}); err != nil {
			return err
		}
		series = append(series, pd)
		return nil
	})
	return series, err
}

// parseCurrencyBytes converts 20 binary currency bytes to a string.
// XRP = all zeros. Standard 3-letter ISO codes are at bytes 12-14. A
// non-standard 160-bit code renders as upper-case hex, matching the binary
// codec's decodeCurrencyCode so the same currency yields an identical string
// whether it reaches us via tx decode or SLE parse — token-pair keys must
// compare equal across both paths.
func parseCurrencyBytes(b []byte) string {
	if len(b) != 20 {
		return strings.ToUpper(hex.EncodeToString(b))
	}

	// Check if all zeros (XRP)
	allZero := true
	for _, v := range b {
		if v != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return "XRP"
	}

	// Check if it's a standard 3-letter code (bytes 12-14 non-zero, rest zero)
	isStandard := true
	for i := range 12 {
		if b[i] != 0 {
			isStandard = false
			break
		}
	}
	if isStandard {
		for i := 15; i < 20; i++ {
			if b[i] != 0 {
				isStandard = false
				break
			}
		}
	}
	if isStandard && b[12] != 0 && b[13] != 0 && b[14] != 0 {
		return string(b[12:15])
	}

	return strings.ToUpper(hex.EncodeToString(b))
}

// SerializeOracle serializes an Oracle ledger entry to binary format.
// Pattern: Go struct → JSON map → binarycodec.Encode() → hex → bytes
func SerializeOracle(o *OracleData) ([]byte, error) {
	ownerAddr, err := addresscodec.EncodeAccountIDToClassicAddress(o.Owner[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}

	jsonObj := map[string]any{
		"LedgerEntryType": "Oracle",
		"Owner":           ownerAddr,
		"Provider":        o.Provider,
		"AssetClass":      o.AssetClass,
		"LastUpdateTime":  o.LastUpdateTime,
		"OwnerNode":       fmt.Sprintf("%X", o.OwnerNode),
		"Flags":           uint32(0),
	}

	if o.URI != "" {
		jsonObj["URI"] = o.URI
	}

	// Emit the threading pointers only when the Oracle has been threaded before
	// (a freshly created Oracle has neither until the apply layer stamps it), so
	// a no-op modification round-trips byte-identically and the apply layer's
	// unchanged-entry guard prunes it (ApplyStateTable.cpp:154-157).
	var emptyHash [32]byte
	if o.PreviousTxnID != emptyHash {
		jsonObj["PreviousTxnID"] = strings.ToUpper(hex.EncodeToString(o.PreviousTxnID[:]))
		jsonObj["PreviousTxnLgrSeq"] = o.PreviousTxnLgrSeq
	}

	// Build PriceDataSeries as []map[string]any
	var series []map[string]any
	for _, pd := range o.PriceDataSeries {
		entry := map[string]any{
			"BaseAsset":  pd.BaseAsset,
			"QuoteAsset": pd.QuoteAsset,
		}
		if pd.HasPrice {
			entry["AssetPrice"] = fmt.Sprintf("%X", pd.AssetPrice)
		}
		if pd.HasScale {
			entry["Scale"] = pd.Scale
		}
		series = append(series, map[string]any{"PriceData": entry})
	}
	jsonObj["PriceDataSeries"] = series

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode Oracle: %w", err)
	}

	return hex.DecodeString(hexStr)
}
