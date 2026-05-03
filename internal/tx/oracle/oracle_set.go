package oracle

import (
	"sort"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/protocol"
)

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

// isFieldPresent returns true if the given field was explicitly present in the
// transaction. It checks both the per-field tracking bools (set by programmatic
// construction) and Common.HasField (set by binary/JSON parsing via ParseFromBinary).
func (o *OracleSet) isFieldPresent(fieldName string) bool {
	switch fieldName {
	case "Provider":
		return o.ProviderPresent || o.GetCommon().HasField("Provider")
	case "URI":
		return o.URIPresent || o.GetCommon().HasField("URI")
	case "AssetClass":
		return o.AssetClassPresent || o.GetCommon().HasField("AssetClass")
	}
	return false
}

// NewOracleSet creates a new OracleSet transaction
func NewOracleSet(account string, oracleDocID uint32, lastUpdateTime uint32) *OracleSet {
	return &OracleSet{
		BaseTx:           *tx.NewBaseTx(tx.TypeOracleSet, account),
		OracleDocumentID: oracleDocID,
		LastUpdateTime:   lastUpdateTime,
	}
}

func (o *OracleSet) TxType() tx.Type {
	return tx.TypeOracleSet
}

// Validate matches rippled's SetOracle::preflight()
func (o *OracleSet) Validate() error {
	if err := o.BaseTx.Validate(); err != nil {
		return err
	}

	if err := tx.CheckFlags(o.GetFlags(), tx.TfUniversalMask); err != nil {
		return err
	}

	// PriceDataSeries must not be empty
	if len(o.PriceDataSeries) == 0 {
		return tx.Errorf(tx.TemARRAY_EMPTY, "PriceDataSeries is required")
	}

	// Max 10 price data entries
	if len(o.PriceDataSeries) > MaxOracleDataSeries {
		return tx.Errorf(tx.TemARRAY_TOO_LARGE, "cannot have more than %d PriceDataSeries entries", MaxOracleDataSeries)
	}

	// Validate Provider field
	// Reference: rippled Oracle_test.cpp lines 229-232 (too long) and 240-242 (empty)
	// If Provider is present (either via isFieldPresent or non-empty string), validate it
	// Note: Provider is stored as hex string, so byte length = len/2
	if o.isFieldPresent("Provider") || o.Provider != "" {
		if len(o.Provider) == 0 {
			return tx.Errorf(tx.TemMALFORMED, "Provider cannot be empty")
		}
		// Provider is hex-encoded, so byte length = string length / 2
		byteLen := len(o.Provider) / 2
		if len(o.Provider)%2 != 0 {
			byteLen = (len(o.Provider) + 1) / 2 // Round up for odd-length strings
		}
		if byteLen > MaxOracleProvider {
			return tx.Errorf(tx.TemMALFORMED, "Provider length must be between 1 and %d bytes", MaxOracleProvider)
		}
	}

	// Validate URI field
	// Reference: rippled Oracle_test.cpp lines 233-235 (too long) and 243-245 (empty)
	// If URI is present (either via isFieldPresent or non-empty string), validate it
	// Note: URI is stored as hex string, so byte length = len/2
	if o.isFieldPresent("URI") || o.URI != "" {
		if len(o.URI) == 0 {
			return tx.Errorf(tx.TemMALFORMED, "URI cannot be empty")
		}
		// URI is hex-encoded, so byte length = string length / 2
		byteLen := len(o.URI) / 2
		if len(o.URI)%2 != 0 {
			byteLen = (len(o.URI) + 1) / 2 // Round up for odd-length strings
		}
		if byteLen > MaxOracleURI {
			return tx.Errorf(tx.TemMALFORMED, "URI length must be between 1 and %d bytes", MaxOracleURI)
		}
	}

	// Validate AssetClass field
	// Reference: rippled Oracle_test.cpp lines 223-228 (too long) and 237-239 (empty)
	// If AssetClass is present (either via isFieldPresent or non-empty string), validate it
	// Note: AssetClass is stored as hex string, so byte length = len/2
	if o.isFieldPresent("AssetClass") || o.AssetClass != "" {
		if len(o.AssetClass) == 0 {
			return tx.Errorf(tx.TemMALFORMED, "AssetClass cannot be empty")
		}
		// AssetClass is hex-encoded, so byte length = string length / 2
		byteLen := len(o.AssetClass) / 2
		if len(o.AssetClass)%2 != 0 {
			byteLen = (len(o.AssetClass) + 1) / 2 // Round up for odd-length strings
		}
		if byteLen > MaxOracleSymbolClass {
			return tx.Errorf(tx.TemMALFORMED, "AssetClass length must be between 1 and %d bytes", MaxOracleSymbolClass)
		}
	}

	// Validate each price data entry and check for duplicates
	// Reference: rippled SetOracle.cpp checkArray()
	seenPairs := make(map[string]bool)
	for _, pd := range o.PriceDataSeries {
		entry := pd.PriceData

		// BaseAsset and QuoteAsset must be different
		if entry.BaseAsset == entry.QuoteAsset {
			return tx.Errorf(tx.TemMALFORMED, "BaseAsset and QuoteAsset must be different")
		}

		// Scale cannot exceed MaxPriceScale
		if entry.Scale != nil && *entry.Scale > MaxPriceScale {
			return tx.Errorf(tx.TemMALFORMED, "Scale cannot exceed %d", MaxPriceScale)
		}

		pairKey := entry.BaseAsset + ":" + entry.QuoteAsset
		if seenPairs[pairKey] {
			return tx.Errorf(tx.TemMALFORMED, "duplicate token pair in PriceDataSeries")
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
			return nil, nil, tx.Errorf(tx.TemMALFORMED, "BaseAsset and QuoteAsset must be different")
		}

		key := entry.TokenPairKey()

		if _, exists := pairsToAdd[key]; exists {
			return nil, nil, tx.Errorf(tx.TemMALFORMED, "duplicate token pair in PriceDataSeries")
		}
		if _, exists := pairsToDelete[key]; exists {
			return nil, nil, tx.Errorf(tx.TemMALFORMED, "duplicate token pair in PriceDataSeries")
		}

		// Validate Scale if present
		if entry.Scale != nil && *entry.Scale > MaxPriceScale {
			return nil, nil, tx.Errorf(tx.TemMALFORMED, "Scale cannot exceed %d", MaxPriceScale)
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
				return nil, nil, tx.Errorf(tx.TemMALFORMED, "cannot delete token pair on oracle creation")
			}
		}
	}

	return pairsToAdd, pairsToDelete, nil
}

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
	ctx.Log.Trace("oracle set apply",
		"account", o.Account,
		"oracleDocumentID", o.OracleDocumentID,
		"provider", o.Provider,
		"priceDataCount", len(o.PriceDataSeries),
	)

	// --- Preclaim: Time validation ---
	// Reference: rippled SetOracle.cpp preclaim lines 80-93
	closeTime := uint64(ctx.Config.ParentCloseTime)
	lastUpdateTime := uint64(o.LastUpdateTime)

	if lastUpdateTime < uint64(protocol.RippleEpochUnix) {
		return tx.TecINVALID_UPDATE_TIME
	}
	lastUpdateTimeEpoch := lastUpdateTime - uint64(protocol.RippleEpochUnix)

	// Validate that lastUpdateTimeEpoch is within maxLastUpdateTimeDelta of closeTime.
	// The lower-bound subtraction (closeTime - delta) is guarded to prevent underflow.
	// In rippled this guard returns tecINTERNAL (LCOV_EXCL_LINE — effectively dead code
	// since close time is always well past 300s in practice). We skip the range check
	// entirely when closeTime is too small, matching the intent of the unreachable guard.
	if closeTime >= MaxLastUpdateTimeDelta {
		if lastUpdateTimeEpoch < (closeTime-MaxLastUpdateTimeDelta) ||
			lastUpdateTimeEpoch > (closeTime+MaxLastUpdateTimeDelta) {
			return tx.TecINVALID_UPDATE_TIME
		}
	}

	// --- Read oracle SLE to determine create vs update ---
	oracleKey := keylet.Oracle(ctx.AccountID, o.OracleDocumentID)
	existingData, readErr := ctx.View.Read(oracleKey)
	isUpdate := readErr == nil && existingData != nil

	// --- Build pair sets from tx PriceDataSeries ---
	// Reference: rippled SetOracle.cpp preclaim lines 98-118
	pairs := make(map[string]pairEntry)   // pairs to add/update
	pairsDel := make(map[string]struct{}) // pairs to delete

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
	var existingOracle *state.OracleData
	var adjustReserve int

	if isUpdate {
		// Reference: rippled SetOracle.cpp preclaim lines 129-158
		var err error
		existingOracle, err = state.ParseOracle(existingData)
		if err != nil {
			return tx.TefINTERNAL
		}

		// LastUpdateTime must be more recent than existing
		if o.LastUpdateTime <= existingOracle.LastUpdateTime {
			return tx.TecINVALID_UPDATE_TIME
		}

		// If field is present in tx, it must match existing value
		if o.isFieldPresent("Provider") && o.Provider != existingOracle.Provider {
			return tx.TemMALFORMED
		}
		if o.isFieldPresent("AssetClass") && o.AssetClass != existingOracle.AssetClass {
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
		if !o.isFieldPresent("Provider") && o.Provider == "" {
			return tx.TemMALFORMED
		}
		if !o.isFieldPresent("AssetClass") && o.AssetClass == "" {
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
		ctx.Log.Warn("oracle set: empty price data after merge")
		return tx.TecARRAY_EMPTY
	}
	if len(pairs) > MaxOracleDataSeries {
		ctx.Log.Warn("oracle set: too many price data entries",
			"count", len(pairs),
		)
		return tx.TecARRAY_TOO_LARGE
	}

	// Reserve check: use prior balance (before fee deduction)
	priorBalance := ctx.Account.Balance + ctx.Config.BaseFee
	reserve := ctx.AccountReserve(uint32(int(ctx.Account.OwnerCount) + adjustReserve))
	if priorBalance < reserve {
		ctx.Log.Warn("oracle set: insufficient reserve",
			"balance", priorBalance,
			"reserve", reserve,
		)
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
	existingOracle *state.OracleData, pairs map[string]pairEntry) tx.Result {
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
			delete(orderedPairs, key)
		} else if existing, ok := orderedPairs[key]; ok {
			existing.price = entry.AssetPrice
			existing.scale = entry.Scale
		} else {
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

	updatedSeries := make([]state.OraclePriceData, 0, len(orderedPairs))
	for _, k := range keys {
		p := orderedPairs[k]
		pd := state.OraclePriceData{
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

	existingOracle.PriceDataSeries = updatedSeries
	existingOracle.LastUpdateTime = o.LastUpdateTime
	if o.isFieldPresent("URI") || o.URI != "" {
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
	data, err := state.SerializeOracle(existingOracle)
	if err != nil {
		ctx.Log.Error("oracle set update: failed to serialize oracle", "error", err)
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(oracleKey, data); err != nil {
		ctx.Log.Error("oracle set update: failed to update oracle", "error", err)
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
	var series []state.OraclePriceData

	if !rules.Enabled(amendment.FeatureFixPriceOracleOrder) {
		// Without fixPriceOracleOrder: use transaction order directly
		for _, pd := range o.PriceDataSeries {
			entry := pd.PriceData
			spd := state.OraclePriceData{
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
			spd := state.OraclePriceData{
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
	oracleData := &state.OracleData{
		Owner:           ctx.AccountID,
		Provider:        o.Provider,
		AssetClass:      o.AssetClass,
		LastUpdateTime:  o.LastUpdateTime,
		PriceDataSeries: series,
	}
	if o.isFieldPresent("URI") || o.URI != "" {
		oracleData.URI = o.URI
	}

	// DirInsert into owner directory
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	dirResult, err := state.DirInsert(ctx.View, ownerDirKey, oracleKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = ctx.AccountID
	})
	if err != nil {
		ctx.Log.Error("oracle set create: directory full", "error", err)
		return tx.TecDIR_FULL
	}
	oracleData.OwnerNode = dirResult.Page

	// Serialize oracle
	data, err := state.SerializeOracle(oracleData)
	if err != nil {
		ctx.Log.Error("oracle set create: failed to serialize oracle", "error", err)
		return tx.TefINTERNAL
	}

	// Insert oracle SLE
	if err := ctx.View.Insert(oracleKey, data); err != nil {
		ctx.Log.Error("oracle set create: failed to insert oracle", "error", err)
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
