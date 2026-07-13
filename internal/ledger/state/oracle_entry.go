package state

import (
	"encoding/hex"
	"fmt"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/tx/ledgerfields"
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
	// OracleDocumentID records a keylet input, stored once
	// fixIncludeKeyletFields is active. A zero id is valid, so presence is
	// tracked separately.
	OracleDocumentID    uint32
	HasOracleDocumentID bool
	// Round-trips so a no-op modify re-serializes byte-identically and the apply
	// layer's unchanged-entry guard prunes it (ApplyStateTable.cpp:154-157).
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

// ParseOracle parses an Oracle ledger entry from binary data.
func ParseOracle(data []byte) (*OracleData, error) {
	var decoded ledgerfields.Oracle
	if err := decoded.Decode(data); err != nil {
		return nil, fmt.Errorf("failed to decode Oracle: %w", err)
	}
	fields := decoded.ToMap()
	oracle := &OracleData{
		Provider:            strings.ToLower(decoded.Provider),
		AssetClass:          strings.ToLower(decoded.AssetClass),
		LastUpdateTime:      decoded.LastUpdateTime,
		URI:                 strings.ToLower(decoded.URI),
		Flags:               decoded.Flags,
		OracleDocumentID:    decoded.OracleDocumentID,
		HasOracleDocumentID: fields["OracleDocumentID"] != nil,
		PreviousTxnLgrSeq:   decoded.PreviousTxnLgrSeq,
	}

	var err error
	if _, ok := fields["Owner"]; ok {
		oracle.Owner, err = decodeLedgerAccount("Oracle.Owner", decoded.Owner)
		if err != nil {
			return nil, err
		}
	}
	if _, ok := fields["OwnerNode"]; ok {
		oracle.OwnerNode, err = parseLedgerUint64("Oracle.OwnerNode", decoded.OwnerNode)
		if err != nil {
			return nil, err
		}
	}
	if _, ok := fields["PreviousTxnID"]; ok {
		if err := decodeLedgerHex("Oracle.PreviousTxnID", decoded.PreviousTxnID, oracle.PreviousTxnID[:]); err != nil {
			return nil, err
		}
	}
	oracle.PriceDataSeries, err = decodeOraclePriceDataSeries(decoded.PriceDataSeries)
	if err != nil {
		return nil, err
	}

	return oracle, nil
}

func decodeOraclePriceDataSeries(values []any) ([]OraclePriceData, error) {
	var series []OraclePriceData
	for i, value := range values {
		wrapper, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Oracle.PriceDataSeries[%d]: expected object, got %T", i, value)
		}
		value, ok = wrapper["PriceData"]
		if !ok {
			continue
		}
		fields, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Oracle.PriceDataSeries[%d].PriceData: expected object, got %T", i, value)
		}

		price := OraclePriceData{}
		if value, ok := fields["BaseAsset"].(string); ok {
			price.BaseAsset = value
		}
		if value, ok := fields["QuoteAsset"].(string); ok {
			price.QuoteAsset = value
		}
		if value, ok := fields["AssetPrice"].(string); ok {
			assetPrice, err := parseLedgerUint64(fmt.Sprintf("Oracle.PriceDataSeries[%d].PriceData.AssetPrice", i), value)
			if err != nil {
				return nil, err
			}
			price.AssetPrice = assetPrice
			price.HasPrice = true
		}
		if value, ok := fields["Scale"].(int); ok {
			if value < 0 || value > 255 {
				return nil, fmt.Errorf("Oracle.PriceDataSeries[%d].PriceData.Scale: decoded value %d is out of range", i, value)
			}
			price.Scale = uint8(value)
			price.HasScale = true
		}
		series = append(series, price)
	}
	return series, nil
}

// SerializeOracle serializes an Oracle ledger entry to binary format.
// The generated ledgerfields writer owns the top-level SLE field set.
func SerializeOracle(o *OracleData) ([]byte, error) {
	ownerAddr, err := addresscodec.EncodeAccountIDToClassicAddress(o.Owner[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}

	entry := &ledgerfields.Oracle{}
	entry.SetOwner(ownerAddr)
	entry.SetProvider(o.Provider)
	entry.SetAssetClass(o.AssetClass)
	entry.SetLastUpdateTime(o.LastUpdateTime)
	entry.SetOwnerNode(fmt.Sprintf("%X", o.OwnerNode))
	entry.SetFlags(o.Flags)

	if o.URI != "" {
		entry.SetURI(o.URI)
	}

	// A zero id is valid, so gate on presence rather than value.
	if o.HasOracleDocumentID {
		entry.SetOracleDocumentID(o.OracleDocumentID)
	}

	// Emit only once threaded; a fresh entry's pointers are stamped by the apply layer.
	var emptyHash [32]byte
	if o.PreviousTxnID != emptyHash {
		entry.SetPreviousTxnID(strings.ToUpper(hex.EncodeToString(o.PreviousTxnID[:])))
		entry.SetPreviousTxnLgrSeq(o.PreviousTxnLgrSeq)
	}

	series := make([]any, 0, len(o.PriceDataSeries))
	for _, pd := range o.PriceDataSeries {
		priceData := map[string]any{
			"BaseAsset":  pd.BaseAsset,
			"QuoteAsset": pd.QuoteAsset,
		}
		if pd.HasPrice {
			priceData["AssetPrice"] = fmt.Sprintf("%X", pd.AssetPrice)
		}
		if pd.HasScale {
			priceData["Scale"] = pd.Scale
		}
		series = append(series, map[string]any{"PriceData": priceData})
	}
	entry.SetPriceDataSeries(series)

	data, err := entry.Encode()
	if err != nil {
		return nil, fmt.Errorf("failed to encode Oracle: %w", err)
	}
	return data, nil
}
