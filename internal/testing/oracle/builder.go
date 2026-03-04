package oracle

import (
	"fmt"
	"strings"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/oracle"
	"github.com/LeJamon/goXRPLd/internal/testing"
)

// XRPLEpochOffset is the XRPL epoch (Jan 1, 2000 00:00:00 UTC) in Unix seconds.
const XRPLEpochOffset = 946684800

// OracleSetBuilder provides a fluent interface for building OracleSet transactions.
type OracleSetBuilder struct {
	account          *testing.Account
	oracleDocumentID uint32
	provider         string
	providerPresent  bool
	uri              string
	uriPresent       bool
	assetClass       string
	assetClassPresent bool
	lastUpdateTime   uint32
	priceDataSeries  []oracle.PriceData
	fee              uint64
	sequence         *uint32
	flags            *uint32
}

// OracleSet creates a new OracleSetBuilder.
// oracleDocumentID identifies this oracle.
// lastUpdateTime is the timestamp of the last update (Unix epoch, must be >= 946684800).
func OracleSet(account *testing.Account, oracleDocumentID uint32, lastUpdateTime uint32) *OracleSetBuilder {
	return &OracleSetBuilder{
		account:          account,
		oracleDocumentID: oracleDocumentID,
		lastUpdateTime:   lastUpdateTime,
		fee:              10, // Default fee: 10 drops
	}
}

// Provider sets the oracle provider name (required for creation).
// The provider string should be hex-encoded.
func (b *OracleSetBuilder) Provider(provider string) *OracleSetBuilder {
	b.provider = provider
	b.providerPresent = true
	return b
}

// ProviderHex sets the provider from a byte length (generates hex string).
func (b *OracleSetBuilder) ProviderHex(byteLen int) *OracleSetBuilder {
	b.provider = strings.Repeat("AB", byteLen)
	b.providerPresent = true
	return b
}

// URI sets the URI for the oracle data (optional).
// The URI string should be hex-encoded.
func (b *OracleSetBuilder) URI(uri string) *OracleSetBuilder {
	b.uri = uri
	b.uriPresent = true
	return b
}

// URIHex sets the URI from a byte length (generates hex string).
func (b *OracleSetBuilder) URIHex(byteLen int) *OracleSetBuilder {
	b.uri = strings.Repeat("AB", byteLen)
	b.uriPresent = true
	return b
}

// AssetClass sets the asset class for pricing (required for creation).
// The asset class string should be hex-encoded.
func (b *OracleSetBuilder) AssetClass(assetClass string) *OracleSetBuilder {
	b.assetClass = assetClass
	b.assetClassPresent = true
	return b
}

// AssetClassHex sets the asset class from a byte length (generates hex string).
func (b *OracleSetBuilder) AssetClassHex(byteLen int) *OracleSetBuilder {
	b.assetClass = strings.Repeat("AB", byteLen)
	b.assetClassPresent = true
	return b
}

// AddPrice adds a price data entry with price and scale.
func (b *OracleSetBuilder) AddPrice(baseAsset, quoteAsset string, price uint64, scale uint8) *OracleSetBuilder {
	p := price
	s := scale
	b.priceDataSeries = append(b.priceDataSeries, oracle.PriceData{
		PriceData: oracle.PriceDataEntry{
			BaseAsset:  baseAsset,
			QuoteAsset: quoteAsset,
			AssetPrice: &p,
			Scale:      &s,
		},
	})
	return b
}

// AddPriceNoScale adds a price data entry with price only (no scale).
func (b *OracleSetBuilder) AddPriceNoScale(baseAsset, quoteAsset string, price uint64) *OracleSetBuilder {
	p := price
	b.priceDataSeries = append(b.priceDataSeries, oracle.PriceData{
		PriceData: oracle.PriceDataEntry{
			BaseAsset:  baseAsset,
			QuoteAsset: quoteAsset,
			AssetPrice: &p,
		},
	})
	return b
}

// AddDelete adds a delete request for a token pair (no price = delete).
func (b *OracleSetBuilder) AddDelete(baseAsset, quoteAsset string) *OracleSetBuilder {
	b.priceDataSeries = append(b.priceDataSeries, oracle.PriceData{
		PriceData: oracle.PriceDataEntry{
			BaseAsset:  baseAsset,
			QuoteAsset: quoteAsset,
			// AssetPrice nil = delete
		},
	})
	return b
}

// Fee sets the transaction fee in drops.
func (b *OracleSetBuilder) Fee(f uint64) *OracleSetBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *OracleSetBuilder) Sequence(seq uint32) *OracleSetBuilder {
	b.sequence = &seq
	return b
}

// Flags sets the transaction flags.
func (b *OracleSetBuilder) Flags(flags uint32) *OracleSetBuilder {
	b.flags = &flags
	return b
}

// Build constructs the OracleSet transaction.
func (b *OracleSetBuilder) Build() tx.Transaction {
	oset := oracle.NewOracleSet(b.account.Address, b.oracleDocumentID, b.lastUpdateTime)
	oset.Common.Fee = fmt.Sprintf("%d", b.fee)

	if b.providerPresent {
		oset.Provider = b.provider
		oset.ProviderPresent = true
	}
	if b.uriPresent {
		oset.URI = b.uri
		oset.URIPresent = true
	}
	if b.assetClassPresent {
		oset.AssetClass = b.assetClass
		oset.AssetClassPresent = true
	}
	if len(b.priceDataSeries) > 0 {
		oset.PriceDataSeries = b.priceDataSeries
	}
	if b.sequence != nil {
		oset.Common.Sequence = b.sequence
	}
	if b.flags != nil {
		oset.Common.Flags = b.flags
	}

	return oset
}

// BuildOracleSet is a convenience method that returns the concrete *oracle.OracleSet type.
func (b *OracleSetBuilder) BuildOracleSet() *oracle.OracleSet {
	return b.Build().(*oracle.OracleSet)
}

// OracleDeleteBuilder provides a fluent interface for building OracleDelete transactions.
type OracleDeleteBuilder struct {
	account          *testing.Account
	oracleDocumentID uint32
	fee              uint64
	sequence         *uint32
	flags            *uint32
}

// OracleDelete creates a new OracleDeleteBuilder.
// oracleDocumentID identifies the oracle to delete.
func OracleDelete(account *testing.Account, oracleDocumentID uint32) *OracleDeleteBuilder {
	return &OracleDeleteBuilder{
		account:          account,
		oracleDocumentID: oracleDocumentID,
		fee:              10, // Default fee: 10 drops
	}
}

// Fee sets the transaction fee in drops.
func (b *OracleDeleteBuilder) Fee(f uint64) *OracleDeleteBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *OracleDeleteBuilder) Sequence(seq uint32) *OracleDeleteBuilder {
	b.sequence = &seq
	return b
}

// Flags sets the transaction flags.
func (b *OracleDeleteBuilder) Flags(flags uint32) *OracleDeleteBuilder {
	b.flags = &flags
	return b
}

// Build constructs the OracleDelete transaction.
func (b *OracleDeleteBuilder) Build() tx.Transaction {
	odel := oracle.NewOracleDelete(b.account.Address, b.oracleDocumentID)
	odel.Common.Fee = fmt.Sprintf("%d", b.fee)

	if b.sequence != nil {
		odel.Common.Sequence = b.sequence
	}
	if b.flags != nil {
		odel.Common.Flags = b.flags
	}

	return odel
}

// BuildOracleDelete is a convenience method that returns the concrete *oracle.OracleDelete type.
func (b *OracleDeleteBuilder) BuildOracleDelete() *oracle.OracleDelete {
	return b.Build().(*oracle.OracleDelete)
}

// DefaultLastUpdateTime returns a valid LastUpdateTime for the given test env.
// It uses the current env clock time as a Unix timestamp.
// Reference: rippled Oracle.h testStartTime = epoch_offset + 10000s
func DefaultLastUpdateTime(env *testing.TestEnv) uint32 {
	return uint32(env.Now().Unix())
}
