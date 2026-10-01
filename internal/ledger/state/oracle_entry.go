package state

import (
	"fmt"
	"strings"

	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
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
	lastUpdateTime, err := decoded.GetLastUpdateTime()
	if err != nil {
		return nil, err
	}
	flags, err := decoded.GetFlags()
	if err != nil {
		return nil, err
	}
	ownerNode, err := decoded.GetOwnerNode()
	if err != nil {
		return nil, err
	}
	owner, err := decoded.GetOwner()
	if err != nil {
		return nil, err
	}
	previousTxnLgrSeq, err := decoded.GetPreviousTxnLgrSeq()
	if err != nil {
		return nil, err
	}
	previousTxnID, err := decoded.GetPreviousTxnID()
	if err != nil {
		return nil, err
	}
	oracle := &OracleData{
		Owner:             owner,
		Provider:          strings.ToLower(decoded.Provider),
		AssetClass:        strings.ToLower(decoded.AssetClass),
		LastUpdateTime:    lastUpdateTime,
		URI:               strings.ToLower(decoded.URI),
		Flags:             flags,
		OwnerNode:         ownerNode,
		PreviousTxnID:     previousTxnID,
		PreviousTxnLgrSeq: previousTxnLgrSeq,
	}
	if decoded.HasOracleDocumentID() {
		oracle.OracleDocumentID, err = decoded.GetOracleDocumentID()
		if err != nil {
			return nil, err
		}
		oracle.HasOracleDocumentID = true
	}
	oracle.PriceDataSeries, err = decodeOraclePriceDataSeries(decoded)
	if err != nil {
		return nil, err
	}

	return oracle, nil
}

func decodeOraclePriceDataSeries(decoded ledgerfields.Oracle) ([]OraclePriceData, error) {
	values, err := decoded.GetPriceDataSeries()
	if err != nil {
		return nil, err
	}
	series := make([]OraclePriceData, 0, len(values))
	for i, value := range values {
		baseAsset, err := value.GetBaseAsset()
		if err != nil {
			return nil, fmt.Errorf("Oracle.PriceDataSeries[%d].PriceData.BaseAsset: %w", i, err)
		}
		quoteAsset, err := value.GetQuoteAsset()
		if err != nil {
			return nil, fmt.Errorf("Oracle.PriceDataSeries[%d].PriceData.QuoteAsset: %w", i, err)
		}
		price := OraclePriceData{BaseAsset: baseAsset, QuoteAsset: quoteAsset}
		if value.HasAssetPrice() {
			price.AssetPrice, err = value.GetAssetPrice()
			if err != nil {
				return nil, fmt.Errorf("Oracle.PriceDataSeries[%d].PriceData.AssetPrice: %w", i, err)
			}
			price.HasPrice = true
		}
		if value.HasScale() {
			price.Scale, err = value.GetScale()
			if err != nil {
				return nil, fmt.Errorf("Oracle.PriceDataSeries[%d].PriceData.Scale: %w", i, err)
			}
			price.HasScale = true
		}
		series = append(series, price)
	}
	return series, nil
}

// SerializeOracle serializes an Oracle ledger entry to binary format.
func SerializeOracle(o *OracleData) ([]byte, error) {
	entry := &ledgerfields.Oracle{}
	if err := entry.SetOwnerValue(o.Owner); err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}
	entry.SetProvider(o.Provider)
	entry.SetAssetClass(o.AssetClass)
	entry.SetLastUpdateTime(o.LastUpdateTime)
	entry.SetOwnerNodeValue(o.OwnerNode)
	entry.SetFlagsValue(o.Flags)

	if o.URI != "" {
		entry.SetURI(o.URI)
	}

	// A zero id is valid, so gate on presence rather than value.
	if o.HasOracleDocumentID {
		entry.SetOracleDocumentID(o.OracleDocumentID)
	}

	entry.SetPreviousTxnIDValue(o.PreviousTxnID)
	entry.SetPreviousTxnLgrSeqValue(o.PreviousTxnLgrSeq)

	series := make([]ledgerfields.PriceDataValue, len(o.PriceDataSeries))
	for i, pd := range o.PriceDataSeries {
		var priceData ledgerfields.PriceDataValue
		priceData.SetBaseAsset(pd.BaseAsset)
		priceData.SetQuoteAsset(pd.QuoteAsset)
		if pd.HasPrice {
			priceData.SetAssetPrice(pd.AssetPrice)
		}
		if pd.HasScale {
			if pd.Scale == 0 {
				return nil, fmt.Errorf("failed to encode Oracle.PriceDataSeries[%d]: default field Scale is explicitly set", i)
			}
			priceData.SetScale(pd.Scale)
		}
		series[i] = priceData
	}
	if err := entry.SetPriceDataSeriesValue(series); err != nil {
		return nil, fmt.Errorf("failed to encode Oracle price data: %w", err)
	}

	data, err := entry.Encode()
	if err != nil {
		return nil, fmt.Errorf("failed to encode Oracle: %w", err)
	}
	return data, nil
}
