package grpc

import (
	"context"
	"encoding/hex"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetLedgerRequest represents a request to get ledger information.
type GetLedgerRequest struct {
	// LedgerSpecifier identifies which ledger to retrieve
	Specifier *LedgerSpecifier

	// IncludeTransactions indicates whether to include transactions
	IncludeTransactions bool

	// IncludeState indicates whether to include state data
	IncludeState bool

	// Binary indicates whether to return data in binary format
	Binary bool
}

// GetLedgerResponse represents the response containing ledger information.
type GetLedgerResponse struct {
	// LedgerIndex is the sequence number of the ledger
	LedgerIndex uint32

	// LedgerHash is the hash of the ledger
	LedgerHash [32]byte

	// ParentHash is the hash of the parent ledger
	ParentHash [32]byte

	// TotalDrops is the total XRP in existence
	TotalDrops uint64

	// CloseTime is the ledger close time in Ripple epoch seconds
	CloseTime uint32

	// Validated indicates if the ledger is validated
	Validated bool

	// Closed indicates if the ledger is closed
	Closed bool

	// HeaderBlob is the serialized ledger header (if Binary is true)
	HeaderBlob []byte

	// TransactionsBlob contains serialized transactions (if requested)
	TransactionsBlob [][]byte

	// StateBlob contains serialized state entries (if requested)
	StateBlob [][]byte
}

// GetLedger retrieves ledger information.
func (s *Server) GetLedger(ctx context.Context, req *GetLedgerRequest) (*GetLedgerResponse, error) {
	if s.ledgerService == nil {
		return nil, status.Error(codes.Internal, "ledger service not available")
	}

	// Resolve ledger from specifier
	ledger, validated, err := ledgerFromSpecifier(req.Specifier, s.ledgerService)
	if err != nil {
		switch err {
		case ErrLedgerNotFound:
			return nil, status.Error(codes.NotFound, "ledger not found")
		case ErrNoValidatedLedger:
			return nil, status.Error(codes.NotFound, "no validated ledger available")
		case ErrNoCurrentLedger:
			return nil, status.Error(codes.NotFound, "no current ledger available")
		case ErrNoClosedLedger:
			return nil, status.Error(codes.NotFound, "no closed ledger available")
		default:
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
	}

	resp := &GetLedgerResponse{
		LedgerIndex: ledger.Sequence(),
		LedgerHash:  ledger.Hash(),
		ParentHash:  ledger.ParentHash(),
		TotalDrops:  ledger.TotalDrops(),
		CloseTime:   toRippleTime(ledger.CloseTime()),
		Validated:   validated,
		Closed:      ledger.IsClosed(),
	}

	// Serialize header if binary format requested
	if req.Binary {
		resp.HeaderBlob = serializeLedgerHeader(ledger.Header())
	}

	// Include state data if requested
	if req.IncludeState {
		var stateBlobs [][]byte
		err := ledger.ForEach(func(key [32]byte, data []byte) bool {
			blob, serErr := serializeLedgerObject(data)
			if serErr == nil {
				stateBlobs = append(stateBlobs, blob)
			}
			return true
		})
		if err != nil {
			return nil, status.Error(codes.Internal, "failed to iterate state")
		}
		resp.StateBlob = stateBlobs
	}

	return resp, nil
}

// GetLedgerEntryRequest represents a request to get a specific ledger entry.
type GetLedgerEntryRequest struct {
	// Specifier identifies which ledger to query
	Specifier *LedgerSpecifier

	// Key is the 32-byte key of the ledger entry
	Key [32]byte

	// Binary indicates whether to return data in binary format
	Binary bool
}

// GetLedgerEntryResponse represents the response containing a ledger entry.
type GetLedgerEntryResponse struct {
	// LedgerIndex is the sequence number of the ledger
	LedgerIndex uint32

	// LedgerHash is the hash of the ledger
	LedgerHash [32]byte

	// Key is the key of the entry
	Key [32]byte

	// EntryBlob is the serialized entry data
	EntryBlob []byte

	// EntryType is the type name of the entry
	EntryType string

	// Validated indicates if the ledger is validated
	Validated bool
}

// GetLedgerEntry retrieves a specific ledger entry by its key.
func (s *Server) GetLedgerEntry(ctx context.Context, req *GetLedgerEntryRequest) (*GetLedgerEntryResponse, error) {
	if s.ledgerService == nil {
		return nil, status.Error(codes.Internal, "ledger service not available")
	}

	// Resolve ledger from specifier
	ledger, validated, err := ledgerFromSpecifier(req.Specifier, s.ledgerService)
	if err != nil {
		switch err {
		case ErrLedgerNotFound:
			return nil, status.Error(codes.NotFound, "ledger not found")
		case ErrNoValidatedLedger:
			return nil, status.Error(codes.NotFound, "no validated ledger available")
		default:
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
	}

	// Get the ledger entry
	result, err := s.ledgerService.GetLedgerEntry(req.Key, "")
	if err != nil {
		if err.Error() == "entry not found" {
			return nil, status.Error(codes.NotFound, "ledger entry not found")
		}
		return nil, status.Error(codes.Internal, "failed to get ledger entry: "+err.Error())
	}

	// Serialize the entry
	blob, err := serializeLedgerObject(result.Node)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to serialize ledger entry")
	}

	// Determine entry type
	typeCode := getLedgerEntryType(result.Node)
	typeName := getLedgerEntryTypeName(typeCode)

	resp := &GetLedgerEntryResponse{
		LedgerIndex: ledger.Sequence(),
		LedgerHash:  ledger.Hash(),
		Key:         req.Key,
		EntryBlob:   blob,
		EntryType:   typeName,
		Validated:   validated,
	}

	return resp, nil
}

// GetLedgerDataRequest represents a request to get ledger state data with pagination.
type GetLedgerDataRequest struct {
	// Specifier identifies which ledger to query
	Specifier *LedgerSpecifier

	// Marker is the pagination marker from a previous response
	Marker string

	// Limit is the maximum number of entries to return
	Limit uint32

	// Binary indicates whether to return data in binary format
	Binary bool
}

// LedgerDataEntry represents a single ledger state entry.
type LedgerDataEntry struct {
	// Key is the 32-byte key of the entry
	Key [32]byte

	// Data is the serialized entry data
	Data []byte

	// EntryType is the type name of the entry
	EntryType string
}

// GetLedgerDataResponse represents the response containing ledger state data.
type GetLedgerDataResponse struct {
	// LedgerIndex is the sequence number of the ledger
	LedgerIndex uint32

	// LedgerHash is the hash of the ledger
	LedgerHash [32]byte

	// Entries contains the ledger state entries
	Entries []LedgerDataEntry

	// Marker is the pagination marker for the next page (empty if no more pages)
	Marker string

	// Validated indicates if the ledger is validated
	Validated bool
}

// GetLedgerData retrieves ledger state data with pagination support.
func (s *Server) GetLedgerData(ctx context.Context, req *GetLedgerDataRequest) (*GetLedgerDataResponse, error) {
	if s.ledgerService == nil {
		return nil, status.Error(codes.Internal, "ledger service not available")
	}

	// Resolve ledger from specifier
	ledger, validated, err := ledgerFromSpecifier(req.Specifier, s.ledgerService)
	if err != nil {
		switch err {
		case ErrLedgerNotFound:
			return nil, status.Error(codes.NotFound, "ledger not found")
		case ErrNoValidatedLedger:
			return nil, status.Error(codes.NotFound, "no validated ledger available")
		default:
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
	}

	// Parse marker if provided
	var startKey [32]byte
	hasMarker := false
	if req.Marker != "" {
		var parseErr error
		startKey, parseErr = parseMarker(req.Marker)
		if parseErr != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid marker format")
		}
		hasMarker = true
	}

	// Set default limit
	limit := req.Limit
	if limit == 0 || limit > 2048 {
		limit = 256
	}

	// Collect entries
	var entries []LedgerDataEntry
	var lastKey [32]byte
	count := uint32(0)
	passedMarker := !hasMarker

	err = ledger.ForEach(func(key [32]byte, data []byte) bool {
		// Skip until we pass the marker
		if !passedMarker {
			if key == startKey {
				passedMarker = true
			}
			return true
		}

		// Check limit
		if count >= limit {
			return false
		}

		// Serialize the entry
		blob, serErr := serializeLedgerObject(data)
		if serErr != nil {
			return true // Skip entries that fail to serialize
		}

		typeCode := getLedgerEntryType(data)
		typeName := getLedgerEntryTypeName(typeCode)

		entries = append(entries, LedgerDataEntry{
			Key:       key,
			Data:      blob,
			EntryType: typeName,
		})

		lastKey = key
		count++
		return true
	})

	if err != nil {
		return nil, status.Error(codes.Internal, "failed to iterate ledger data")
	}

	resp := &GetLedgerDataResponse{
		LedgerIndex: ledger.Sequence(),
		LedgerHash:  ledger.Hash(),
		Entries:     entries,
		Validated:   validated,
	}

	// Set marker if there are more entries
	if count >= limit {
		resp.Marker = formatMarker(lastKey)
	}

	return resp, nil
}

// GetLedgerDiffRequest represents a request to get the difference between two ledgers.
type GetLedgerDiffRequest struct {
	// BaseLedgerSpecifier identifies the base ledger
	BaseLedgerSpecifier *LedgerSpecifier

	// DesiredLedgerSpecifier identifies the desired ledger to compare against
	DesiredLedgerSpecifier *LedgerSpecifier

	// IncludeBlobs indicates whether to include the actual data blobs
	IncludeBlobs bool
}

// LedgerDiffEntry represents a single difference between two ledgers.
type LedgerDiffEntry struct {
	// Key is the 32-byte key of the entry
	Key [32]byte

	// DiffType is the type of difference: "created", "modified", "deleted"
	DiffType string

	// OldData is the data in the base ledger (nil for created entries)
	OldData []byte

	// NewData is the data in the desired ledger (nil for deleted entries)
	NewData []byte
}

// GetLedgerDiffResponse represents the response containing ledger differences.
type GetLedgerDiffResponse struct {
	// BaseLedgerIndex is the sequence number of the base ledger
	BaseLedgerIndex uint32

	// BaseLedgerHash is the hash of the base ledger
	BaseLedgerHash [32]byte

	// DesiredLedgerIndex is the sequence number of the desired ledger
	DesiredLedgerIndex uint32

	// DesiredLedgerHash is the hash of the desired ledger
	DesiredLedgerHash [32]byte

	// Differences contains the list of differences
	Differences []LedgerDiffEntry
}

// GetLedgerDiff retrieves the differences between two ledgers.
// This is useful for incremental ledger synchronization.
func (s *Server) GetLedgerDiff(ctx context.Context, req *GetLedgerDiffRequest) (*GetLedgerDiffResponse, error) {
	if s.ledgerService == nil {
		return nil, status.Error(codes.Internal, "ledger service not available")
	}

	// Resolve base ledger
	baseLedger, _, err := ledgerFromSpecifier(req.BaseLedgerSpecifier, s.ledgerService)
	if err != nil {
		return nil, status.Error(codes.NotFound, "base ledger not found: "+err.Error())
	}

	// Resolve desired ledger
	desiredLedger, _, err := ledgerFromSpecifier(req.DesiredLedgerSpecifier, s.ledgerService)
	if err != nil {
		return nil, status.Error(codes.NotFound, "desired ledger not found: "+err.Error())
	}

	// Collect entries from both ledgers
	baseEntries := make(map[[32]byte][]byte)
	desiredEntries := make(map[[32]byte][]byte)

	// Collect base ledger entries
	err = baseLedger.ForEach(func(key [32]byte, data []byte) bool {
		if req.IncludeBlobs {
			dataCopy := make([]byte, len(data))
			copy(dataCopy, data)
			baseEntries[key] = dataCopy
		} else {
			baseEntries[key] = nil
		}
		return true
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to iterate base ledger")
	}

	// Collect desired ledger entries
	err = desiredLedger.ForEach(func(key [32]byte, data []byte) bool {
		if req.IncludeBlobs {
			dataCopy := make([]byte, len(data))
			copy(dataCopy, data)
			desiredEntries[key] = dataCopy
		} else {
			desiredEntries[key] = nil
		}
		return true
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to iterate desired ledger")
	}

	// Calculate differences
	var differences []LedgerDiffEntry

	// Check for created and modified entries
	for key, newData := range desiredEntries {
		oldData, exists := baseEntries[key]
		if !exists {
			// Entry was created
			differences = append(differences, LedgerDiffEntry{
				Key:      key,
				DiffType: "created",
				NewData:  newData,
			})
		} else if req.IncludeBlobs && !bytesEqual(oldData, newData) {
			// Entry was modified
			differences = append(differences, LedgerDiffEntry{
				Key:      key,
				DiffType: "modified",
				OldData:  oldData,
				NewData:  newData,
			})
		}
	}

	// Check for deleted entries
	for key, oldData := range baseEntries {
		if _, exists := desiredEntries[key]; !exists {
			differences = append(differences, LedgerDiffEntry{
				Key:      key,
				DiffType: "deleted",
				OldData:  oldData,
			})
		}
	}

	resp := &GetLedgerDiffResponse{
		BaseLedgerIndex:    baseLedger.Sequence(),
		BaseLedgerHash:     baseLedger.Hash(),
		DesiredLedgerIndex: desiredLedger.Sequence(),
		DesiredLedgerHash:  desiredLedger.Hash(),
		Differences:        differences,
	}

	return resp, nil
}

// bytesEqual compares two byte slices for equality.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// GetLedgerObjectRequest is a convenience request for getting objects by different specifiers.
type GetLedgerObjectRequest struct {
	// Specifier identifies which ledger to query
	Specifier *LedgerSpecifier

	// One of the following should be set:
	Index         string // Direct object index (32-byte hex)
	AccountRoot   string // Account address
	Check         string // Check object ID
	Escrow        *EscrowLocator
	Offer         *OfferLocator
	RippleState   *RippleStateLocator
	PayChannel    string // Payment channel ID
	DirectoryNode string // Directory node ID

	// Binary indicates whether to return data in binary format
	Binary bool
}

// EscrowLocator identifies an escrow object.
type EscrowLocator struct {
	Owner    string
	Sequence uint32
}

// OfferLocator identifies an offer object.
type OfferLocator struct {
	Account  string
	Sequence uint32
}

// RippleStateLocator identifies a trust line.
type RippleStateLocator struct {
	Accounts [2]string
	Currency string
}

// GetLedgerObjectResponse represents the response containing a ledger object.
type GetLedgerObjectResponse struct {
	// Index is the 32-byte key of the object
	Index string

	// LedgerIndex is the sequence number of the ledger
	LedgerIndex uint32

	// LedgerHash is the hash of the ledger
	LedgerHash [32]byte

	// ObjectBlob is the serialized object data
	ObjectBlob []byte

	// ObjectType is the type name of the object
	ObjectType string

	// Validated indicates if the ledger is validated
	Validated bool
}

// GetLedgerObject retrieves a ledger object by various locators.
// This is a convenience method that wraps GetLedgerEntry with keylet computation.
func (s *Server) GetLedgerObject(ctx context.Context, req *GetLedgerObjectRequest) (*GetLedgerObjectResponse, error) {
	if s.ledgerService == nil {
		return nil, status.Error(codes.Internal, "ledger service not available")
	}

	// Resolve the object key based on the locator type
	var key [32]byte

	switch {
	case req.Index != "":
		// Direct index lookup
		decoded, err := hex.DecodeString(req.Index)
		if err != nil || len(decoded) != 32 {
			return nil, status.Error(codes.InvalidArgument, "invalid index: must be 64-character hex string")
		}
		copy(key[:], decoded)

	case req.Check != "":
		decoded, err := hex.DecodeString(req.Check)
		if err != nil || len(decoded) != 32 {
			return nil, status.Error(codes.InvalidArgument, "invalid check ID")
		}
		copy(key[:], decoded)

	case req.PayChannel != "":
		decoded, err := hex.DecodeString(req.PayChannel)
		if err != nil || len(decoded) != 32 {
			return nil, status.Error(codes.InvalidArgument, "invalid payment channel ID")
		}
		copy(key[:], decoded)

	case req.DirectoryNode != "":
		decoded, err := hex.DecodeString(req.DirectoryNode)
		if err != nil || len(decoded) != 32 {
			return nil, status.Error(codes.InvalidArgument, "invalid directory node ID")
		}
		copy(key[:], decoded)

	// Note: AccountRoot, Escrow, Offer, and RippleState would require keylet computation
	// which depends on the address codec and keylet packages. For now, we only support
	// direct index lookups for these types.
	case req.AccountRoot != "":
		return nil, status.Error(codes.Unimplemented, "account root lookup by address not yet implemented")

	case req.Escrow != nil:
		return nil, status.Error(codes.Unimplemented, "escrow lookup not yet implemented")

	case req.Offer != nil:
		return nil, status.Error(codes.Unimplemented, "offer lookup not yet implemented")

	case req.RippleState != nil:
		return nil, status.Error(codes.Unimplemented, "ripple state lookup not yet implemented")

	default:
		return nil, status.Error(codes.InvalidArgument, "must specify object locator")
	}

	// Use GetLedgerEntry to retrieve the object
	entryReq := &GetLedgerEntryRequest{
		Specifier: req.Specifier,
		Key:       key,
		Binary:    req.Binary,
	}

	entryResp, err := s.GetLedgerEntry(ctx, entryReq)
	if err != nil {
		return nil, err
	}

	resp := &GetLedgerObjectResponse{
		Index:       formatHash(key),
		LedgerIndex: entryResp.LedgerIndex,
		LedgerHash:  entryResp.LedgerHash,
		ObjectBlob:  entryResp.EntryBlob,
		ObjectType:  entryResp.EntryType,
		Validated:   entryResp.Validated,
	}

	return resp, nil
}
