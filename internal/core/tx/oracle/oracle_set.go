package oracle

import (
	"errors"
	"fmt"
	"sort"

	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypeOracleSet, func() tx.Transaction {
		return &OracleSet{BaseTx: *tx.NewBaseTx(tx.TypeOracleSet, "")}
	})
}

// OracleSet creates or updates a price oracle.
type OracleSet struct {
	tx.BaseTx

	// OracleDocumentID identifies this oracle (required)
	OracleDocumentID uint32 `json:"OracleDocumentID" xrpl:"OracleDocumentID"`

	// Provider is the oracle provider name (required for creation)
	Provider string `json:"Provider,omitempty" xrpl:"Provider,omitempty"`

	// URI is the URI for the oracle data (optional)
	URI string `json:"URI,omitempty" xrpl:"URI,omitempty"`

	// AssetClass is the asset class for pricing (required for creation)
	AssetClass string `json:"AssetClass,omitempty" xrpl:"AssetClass,omitempty"`

	// LastUpdateTime is the timestamp of the last update (required)
	// This is in Ripple epoch (seconds since Jan 1, 2000)
	LastUpdateTime uint32 `json:"LastUpdateTime" xrpl:"LastUpdateTime"`

	// PriceDataSeries is the price data (required)
	PriceDataSeries []PriceData `json:"PriceDataSeries" xrpl:"PriceDataSeries"`

	// Presence tracking fields - used to detect explicitly empty fields
	// In rippled, an explicitly set empty field (sfURI present but empty) is invalid
	// These are set during JSON/binary deserialization
	ProviderPresent   bool `json:"-" xrpl:"-"` // true if Provider field was explicitly set
	URIPresent        bool `json:"-" xrpl:"-"` // true if URI field was explicitly set
	AssetClassPresent bool `json:"-" xrpl:"-"` // true if AssetClass field was explicitly set
}

// NewOracleSet creates a new OracleSet transaction
func NewOracleSet(account string, oracleDocID uint32, lastUpdateTime uint32) *OracleSet {
	return &OracleSet{
		BaseTx:           *tx.NewBaseTx(tx.TypeOracleSet, account),
		OracleDocumentID: oracleDocID,
		LastUpdateTime:   lastUpdateTime,
	}
}

// TxType returns the transaction type
func (o *OracleSet) TxType() tx.Type {
	return tx.TypeOracleSet
}

// Validate validates the OracleSet transaction (preflight validation)
// This matches rippled's SetOracle::preflight()
func (o *OracleSet) Validate() error {
	if err := o.BaseTx.Validate(); err != nil {
		return err
	}

	// Check flags - only universal flags allowed
	if o.Flags != nil && *o.Flags&tx.TfUniversalMask != 0 {
		return errors.New("temINVALID_FLAG: invalid flags set")
	}

	// PriceDataSeries must not be empty
	if len(o.PriceDataSeries) == 0 {
		return errors.New("temARRAY_EMPTY: PriceDataSeries is required")
	}

	// Max 10 price data entries
	if len(o.PriceDataSeries) > MaxOracleDataSeries {
		return fmt.Errorf("temARRAY_TOO_LARGE: cannot have more than %d PriceDataSeries entries", MaxOracleDataSeries)
	}

	// Validate Provider field
	// Reference: rippled Oracle_test.cpp lines 229-232 (too long) and 240-242 (empty)
	// If Provider is present (either via ProviderPresent flag or non-empty string), validate it
	// Note: Provider is stored as hex string, so byte length = len/2
	if o.ProviderPresent || o.Provider != "" {
		if len(o.Provider) == 0 {
			return errors.New("temMALFORMED: Provider cannot be empty")
		}
		// Provider is hex-encoded, so byte length = string length / 2
		byteLen := len(o.Provider) / 2
		if len(o.Provider)%2 != 0 {
			byteLen = (len(o.Provider) + 1) / 2 // Round up for odd-length strings
		}
		if byteLen > MaxOracleProvider {
			return fmt.Errorf("temMALFORMED: Provider length must be between 1 and %d bytes", MaxOracleProvider)
		}
	}

	// Validate URI field
	// Reference: rippled Oracle_test.cpp lines 233-235 (too long) and 243-245 (empty)
	// If URI is present (either via URIPresent flag or non-empty string), validate it
	// Note: URI is stored as hex string, so byte length = len/2
	if o.URIPresent || o.URI != "" {
		if len(o.URI) == 0 {
			return errors.New("temMALFORMED: URI cannot be empty")
		}
		// URI is hex-encoded, so byte length = string length / 2
		byteLen := len(o.URI) / 2
		if len(o.URI)%2 != 0 {
			byteLen = (len(o.URI) + 1) / 2 // Round up for odd-length strings
		}
		if byteLen > MaxOracleURI {
			return fmt.Errorf("temMALFORMED: URI length must be between 1 and %d bytes", MaxOracleURI)
		}
	}

	// Validate AssetClass field
	// Reference: rippled Oracle_test.cpp lines 223-228 (too long) and 237-239 (empty)
	// If AssetClass is present (either via AssetClassPresent flag or non-empty string), validate it
	// Note: AssetClass is stored as hex string, so byte length = len/2
	if o.AssetClassPresent || o.AssetClass != "" {
		if len(o.AssetClass) == 0 {
			return errors.New("temMALFORMED: AssetClass cannot be empty")
		}
		// AssetClass is hex-encoded, so byte length = string length / 2
		byteLen := len(o.AssetClass) / 2
		if len(o.AssetClass)%2 != 0 {
			byteLen = (len(o.AssetClass) + 1) / 2 // Round up for odd-length strings
		}
		if byteLen > MaxOracleSymbolClass {
			return fmt.Errorf("temMALFORMED: AssetClass length must be between 1 and %d bytes", MaxOracleSymbolClass)
		}
	}

	// Validate each price data entry and check for duplicates
	// Reference: rippled SetOracle.cpp checkArray()
	seenPairs := make(map[string]bool)
	for _, pd := range o.PriceDataSeries {
		entry := pd.PriceData

		// BaseAsset and QuoteAsset must be different
		if entry.BaseAsset == entry.QuoteAsset {
			return errors.New("temMALFORMED: BaseAsset and QuoteAsset must be different")
		}

		// Scale cannot exceed MaxPriceScale
		if entry.Scale != nil && *entry.Scale > MaxPriceScale {
			return fmt.Errorf("temMALFORMED: Scale cannot exceed %d", MaxPriceScale)
		}

		// Check for duplicate token pairs
		pairKey := entry.BaseAsset + ":" + entry.QuoteAsset
		if seenPairs[pairKey] {
			return errors.New("temMALFORMED: duplicate token pair in PriceDataSeries")
		}
		seenPairs[pairKey] = true
	}

	return nil
}

// ValidatePriceDataSeries performs detailed validation of the price data series
// This is called during preclaim when we know whether this is a create or update
// Returns: pairsToAdd, pairsToDelete, error
func (o *OracleSet) ValidatePriceDataSeries(isUpdate bool) (map[string]PriceDataEntry, map[string]struct{}, error) {
	pairsToAdd := make(map[string]PriceDataEntry)
	pairsToDelete := make(map[string]struct{})

	for _, pd := range o.PriceDataSeries {
		entry := pd.PriceData

		// BaseAsset and QuoteAsset must be different
		if entry.BaseAsset == entry.QuoteAsset {
			return nil, nil, errors.New("temMALFORMED: BaseAsset and QuoteAsset must be different")
		}

		key := entry.TokenPairKey()

		// Check for duplicates in both sets
		if _, exists := pairsToAdd[key]; exists {
			return nil, nil, errors.New("temMALFORMED: duplicate token pair in PriceDataSeries")
		}
		if _, exists := pairsToDelete[key]; exists {
			return nil, nil, errors.New("temMALFORMED: duplicate token pair in PriceDataSeries")
		}

		// Validate Scale if present
		if entry.Scale != nil && *entry.Scale > MaxPriceScale {
			return nil, nil, fmt.Errorf("temMALFORMED: Scale cannot exceed %d", MaxPriceScale)
		}

		if entry.AssetPrice != nil {
			// This is an add/update operation
			pairsToAdd[key] = entry
		} else {
			// This is a delete operation (AssetPrice not present)
			if isUpdate {
				pairsToDelete[key] = struct{}{}
			} else {
				// Cannot delete on create
				return nil, nil, errors.New("temMALFORMED: cannot delete token pair on oracle creation")
			}
		}
	}

	return pairsToAdd, pairsToDelete, nil
}

// Flatten returns a flat map of all transaction fields
func (o *OracleSet) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(o)
}

// AddPriceData adds a price data entry with price and scale
func (o *OracleSet) AddPriceData(baseAsset, quoteAsset string, price uint64, scale uint8) {
	o.PriceDataSeries = append(o.PriceDataSeries, PriceData{
		PriceData: PriceDataEntry{
			BaseAsset:  baseAsset,
			QuoteAsset: quoteAsset,
			AssetPrice: &price,
			Scale:      &scale,
		},
	})
}

// AddPriceDataDelete adds a delete request for a token pair
func (o *OracleSet) AddPriceDataDelete(baseAsset, quoteAsset string) {
	o.PriceDataSeries = append(o.PriceDataSeries, PriceData{
		PriceData: PriceDataEntry{
			BaseAsset:  baseAsset,
			QuoteAsset: quoteAsset,
			// AssetPrice and Scale are nil, indicating delete
		},
	})
}

// RequiredAmendments returns the amendments required for this transaction type
func (o *OracleSet) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeaturePriceOracle}
}

// pairEntry holds a price data pair during preclaim/doApply processing.
type pairEntry struct {
	baseAsset  string
	quoteAsset string
	price      *uint64
	scale      *uint8
}

// Apply applies an OracleSet transaction to the ledger state.
// Combines rippled's SetOracle::preclaim() and SetOracle::doApply().
// Reference: rippled SetOracle.cpp
func (o *OracleSet) Apply(ctx *tx.ApplyContext) tx.Result {
	// --- Preclaim: Time validation ---
	// Reference: rippled SetOracle.cpp preclaim lines 80-93
	closeTime := ctx.Config.ParentCloseTime
	lastUpdateTime := uint64(o.LastUpdateTime)

	if lastUpdateTime < RippleEpochOffset {
		return tx.TecINVALID_UPDATE_TIME
	}
	lastUpdateTimeEpoch := lastUpdateTime - RippleEpochOffset

	if uint64(closeTime) < MaxLastUpdateTimeDelta {
		return tx.TefINTERNAL
	}
	if lastUpdateTimeEpoch < (uint64(closeTime)-MaxLastUpdateTimeDelta) ||
		lastUpdateTimeEpoch > (uint64(closeTime)+MaxLastUpdateTimeDelta) {
		return tx.TecINVALID_UPDATE_TIME
	}

	// --- Read oracle SLE to determine create vs update ---
	oracleKey := keylet.Oracle(ctx.AccountID, o.OracleDocumentID)
	existingData, readErr := ctx.View.Read(oracleKey)
	isUpdate := readErr == nil && existingData != nil

	// --- Build pair sets from tx PriceDataSeries ---
	// Reference: rippled SetOracle.cpp preclaim lines 98-118
	pairs := make(map[string]pairEntry)    // pairs to add/update
	pairsDel := make(map[string]struct{})  // pairs to delete

	for _, pd := range o.PriceDataSeries {
		entry := pd.PriceData

		if entry.BaseAsset == entry.QuoteAsset {
			return tx.TemMALFORMED
		}

		key := entry.TokenPairKey()
		if _, exists := pairs[key]; exists {
			return tx.TemMALFORMED
		}
		if _, exists := pairsDel[key]; exists {
			return tx.TemMALFORMED
		}

		if entry.Scale != nil && *entry.Scale > MaxPriceScale {
			return tx.TemMALFORMED
		}

		if entry.AssetPrice != nil {
			pairs[key] = pairEntry{
				baseAsset:  entry.BaseAsset,
				quoteAsset: entry.QuoteAsset,
				price:      entry.AssetPrice,
				scale:      entry.Scale,
			}
		} else if isUpdate {
			pairsDel[key] = struct{}{}
		} else {
			return tx.TemMALFORMED
		}
	}

	// --- Update-specific preclaim ---
	var existingOracle *sle.OracleData
	var adjustReserve int

	if isUpdate {
		// Reference: rippled SetOracle.cpp preclaim lines 129-158
		var err error
		existingOracle, err = sle.ParseOracle(existingData)
		if err != nil {
			return tx.TefINTERNAL
		}

		// LastUpdateTime must be more recent than existing
		if o.LastUpdateTime <= existingOracle.LastUpdateTime {
			return tx.TecINVALID_UPDATE_TIME
		}

		// Check provider/assetClass consistency
		// If field is present in tx, it must match existing value
		if o.ProviderPresent && o.Provider != existingOracle.Provider {
			return tx.TemMALFORMED
		}
		if o.AssetClassPresent && o.AssetClass != existingOracle.AssetClass {
			return tx.TemMALFORMED
		}

		// Merge existing pairs with tx pairs
		for _, existing := range existingOracle.PriceDataSeries {
			key := existing.BaseAsset + "/" + existing.QuoteAsset
			if _, inPairs := pairs[key]; !inPairs {
				// Not in tx add set — check if it's being deleted
				if _, inDel := pairsDel[key]; inDel {
					delete(pairsDel, key)
				} else {
					// Keep existing pair (without price/scale update)
					pairs[key] = pairEntry{
						baseAsset:  existing.BaseAsset,
						quoteAsset: existing.QuoteAsset,
					}
				}
			}
		}

		if len(pairsDel) > 0 {
			return tx.TecTOKEN_PAIR_NOT_FOUND
		}

		oldCount := 1
		if len(existingOracle.PriceDataSeries) > 5 {
			oldCount = 2
		}
		newCount := 1
		if len(pairs) > 5 {
			newCount = 2
		}
		adjustReserve = newCount - oldCount
	} else {
		// --- Create-specific preclaim ---
		// Reference: rippled SetOracle.cpp preclaim lines 160-168
		if !o.ProviderPresent && o.Provider == "" {
			return tx.TemMALFORMED
		}
		if !o.AssetClassPresent && o.AssetClass == "" {
			return tx.TemMALFORMED
		}

		if len(pairs) > 5 {
			adjustReserve = 2
		} else {
			adjustReserve = 1
		}
	}

	// --- Final preclaim checks ---
	// Reference: rippled SetOracle.cpp preclaim lines 170-181
	if len(pairs) == 0 {
		return tx.TecARRAY_EMPTY
	}
	if len(pairs) > MaxOracleDataSeries {
		return tx.TecARRAY_TOO_LARGE
	}

	// Reserve check: use prior balance (before fee deduction)
	priorBalance := ctx.Account.Balance + ctx.Config.BaseFee
	reserve := ctx.AccountReserve(uint32(int(ctx.Account.OwnerCount) + adjustReserve))
	if priorBalance < reserve {
		return tx.TecINSUFFICIENT_RESERVE
	}

	// ========== doApply ==========

	if isUpdate {
		// Reference: rippled SetOracle.cpp doApply lines 223-280
		return o.doApplyUpdate(ctx, oracleKey, existingOracle, pairs)
	}
	return o.doApplyCreate(ctx, oracleKey, pairs)
}

// doApplyUpdate applies an OracleSet update to an existing oracle.
// Reference: rippled SetOracle.cpp doApply lines 223-280
func (o *OracleSet) doApplyUpdate(ctx *tx.ApplyContext, oracleKey keylet.Keylet,
	existingOracle *sle.OracleData, pairs map[string]pairEntry) tx.Result {

	// Build ordered pairs map from existing PriceDataSeries.
	// Existing pairs are stored WITHOUT price/scale (just base/quote).
	type orderedPair struct {
		baseAsset  string
		quoteAsset string
		price      *uint64
		scale      *uint8
	}
	orderedPairs := make(map[string]*orderedPair)
	for _, existing := range existingOracle.PriceDataSeries {
		key := existing.BaseAsset + "/" + existing.QuoteAsset
		orderedPairs[key] = &orderedPair{
			baseAsset:  existing.BaseAsset,
			quoteAsset: existing.QuoteAsset,
		}
	}
	oldCount := 1
	if len(orderedPairs) > 5 {
		oldCount = 2
	}

	// Apply tx changes: delete, update, or add
	for _, pd := range o.PriceDataSeries {
		entry := pd.PriceData
		key := entry.TokenPairKey()

		if entry.AssetPrice == nil {
			// Delete pair
			delete(orderedPairs, key)
		} else if existing, ok := orderedPairs[key]; ok {
			// Update existing pair
			existing.price = entry.AssetPrice
			existing.scale = entry.Scale
		} else {
			// Add new pair
			orderedPairs[key] = &orderedPair{
				baseAsset:  entry.BaseAsset,
				quoteAsset: entry.QuoteAsset,
				price:      entry.AssetPrice,
				scale:      entry.Scale,
			}
		}
	}

	// Build updated PriceDataSeries (map iteration = sorted by key in Go maps... but Go maps are NOT ordered)
	// In rippled, std::map sorts by key. We need sorted order.
	keys := make([]string, 0, len(orderedPairs))
	for k := range orderedPairs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	updatedSeries := make([]sle.OraclePriceData, 0, len(orderedPairs))
	for _, k := range keys {
		p := orderedPairs[k]
		pd := sle.OraclePriceData{
			BaseAsset:  p.baseAsset,
			QuoteAsset: p.quoteAsset,
		}
		if p.price != nil {
			pd.AssetPrice = *p.price
			pd.HasPrice = true
		}
		if p.scale != nil {
			pd.Scale = *p.scale
			pd.HasScale = true
		}
		updatedSeries = append(updatedSeries, pd)
	}

	// Update oracle SLE fields
	existingOracle.PriceDataSeries = updatedSeries
	existingOracle.LastUpdateTime = o.LastUpdateTime
	if o.URIPresent || o.URI != "" {
		existingOracle.URI = o.URI
	}

	// Adjust OwnerCount
	newCount := 1
	if len(updatedSeries) > 5 {
		newCount = 2
	}
	adjust := newCount - oldCount
	if adjust > 0 {
		ctx.Account.OwnerCount += uint32(adjust)
	} else if adjust < 0 {
		ctx.Account.OwnerCount -= uint32(-adjust)
	}

	// Serialize and write back
	data, err := sle.SerializeOracle(existingOracle)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(oracleKey, data); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// doApplyCreate applies an OracleSet create for a new oracle.
// Reference: rippled SetOracle.cpp doApply lines 282-327
func (o *OracleSet) doApplyCreate(ctx *tx.ApplyContext, oracleKey keylet.Keylet,
	pairs map[string]pairEntry) tx.Result {
	rules := ctx.Rules()

	// Build PriceDataSeries
	var series []sle.OraclePriceData

	if !rules.Enabled(amendment.FeatureFixPriceOracleOrder) {
		// Without fixPriceOracleOrder: use transaction order directly
		for _, pd := range o.PriceDataSeries {
			entry := pd.PriceData
			spd := sle.OraclePriceData{
				BaseAsset:  entry.BaseAsset,
				QuoteAsset: entry.QuoteAsset,
			}
			if entry.AssetPrice != nil {
				spd.AssetPrice = *entry.AssetPrice
				spd.HasPrice = true
			}
			if entry.Scale != nil {
				spd.Scale = *entry.Scale
				spd.HasScale = true
			}
			series = append(series, spd)
		}
	} else {
		// With fixPriceOracleOrder: sort by (BaseAsset, QuoteAsset) key
		keys := make([]string, 0, len(pairs))
		for k := range pairs {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, k := range keys {
			p := pairs[k]
			spd := sle.OraclePriceData{
				BaseAsset:  p.baseAsset,
				QuoteAsset: p.quoteAsset,
			}
			if p.price != nil {
				spd.AssetPrice = *p.price
				spd.HasPrice = true
			}
			if p.scale != nil {
				spd.Scale = *p.scale
				spd.HasScale = true
			}
			series = append(series, spd)
		}
	}

	// Build oracle SLE
	oracleData := &sle.OracleData{
		Owner:           ctx.AccountID,
		Provider:        o.Provider,
		AssetClass:      o.AssetClass,
		LastUpdateTime:  o.LastUpdateTime,
		PriceDataSeries: series,
	}
	if o.URIPresent || o.URI != "" {
		oracleData.URI = o.URI
	}

	// DirInsert into owner directory
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	dirResult, err := sle.DirInsert(ctx.View, ownerDirKey, oracleKey.Key, func(dir *sle.DirectoryNode) {
		dir.Owner = ctx.AccountID
	})
	if err != nil {
		return tx.TecDIR_FULL
	}
	oracleData.OwnerNode = dirResult.Page

	// Serialize oracle
	data, err := sle.SerializeOracle(oracleData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Insert oracle SLE
	if err := ctx.View.Insert(oracleKey, data); err != nil {
		return tx.TefINTERNAL
	}

	// Adjust OwnerCount
	count := uint32(1)
	if len(series) > 5 {
		count = 2
	}
	ctx.Account.OwnerCount += count

	return tx.TesSUCCESS
}
