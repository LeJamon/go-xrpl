package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/tx/ledgerfields"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

const xrpLedgerEarliestFees uint32 = 562177

func (s *Service) loadLedgerFile(
	ctx context.Context,
	path string,
	defaultCloseTime time.Time,
) (*ledger.Ledger, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open ledger file %q: %w", path, err)
	}
	defer file.Close()

	loaded, err := s.loadLedgerJSON(ctx, file, defaultCloseTime)
	if err != nil {
		return nil, fmt.Errorf("load ledger file %q: %w", path, err)
	}
	return loaded, nil
}

func (s *Service) loadLedgerJSON(
	ctx context.Context,
	r io.Reader,
	defaultCloseTime time.Time,
) (*ledger.Ledger, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(r)
	decoder.UseNumber()

	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode ledger JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("decode ledger JSON: multiple JSON values")
		}
		return nil, fmt.Errorf("decode ledger JSON trailing data: %w", err)
	}

	selected := document
	if object, ok := selected.(map[string]any); ok {
		if result, exists := object["result"]; exists {
			selected = result
		}
	}
	if object, ok := selected.(map[string]any); ok {
		if ledgerValue, exists := object["ledger"]; exists {
			selected = ledgerValue
		}
	}
	selected, err := normalizeLedgerJSONNumbers(selected)
	if err != nil {
		return nil, err
	}

	sequence := uint32(1)
	closeTime := protocol.FromRippleTime(protocol.ToRippleTime(defaultCloseTime))
	closeTimeResolution := uint32(30)
	closeTimeEstimated := false
	totalDrops := uint64(0)
	stateValue := selected

	if object, ok := selected.(map[string]any); ok {
		accountState, exists := object["accountState"]
		if !exists {
			return nil, errors.New("ledger JSON state nodes must be an array")
		}
		stateValue = accountState

		if value, exists := object["ledger_index"]; exists {
			sequence, err = ledgerFileUint32("ledger_index", value)
			if err != nil {
				return nil, err
			}
		}
		if value, exists := object["close_time"]; exists {
			seconds, parseErr := ledgerFileUint32("close_time", value)
			if parseErr != nil {
				return nil, parseErr
			}
			closeTime = protocol.FromRippleTime(seconds)
		}
		if value, exists := object["close_time_resolution"]; exists {
			closeTimeResolution, err = ledgerFileUint32("close_time_resolution", value)
			if err != nil {
				return nil, err
			}
		}
		if value, exists := object["close_time_estimated"]; exists {
			closeTimeEstimated = ledgerFileBool(value)
		}
		if value, exists := object["total_coins"]; exists {
			totalDrops, err = ledgerFileUint64("total_coins", value)
			if err != nil {
				return nil, err
			}
		}
	}

	var stateNodes []any
	switch value := stateValue.(type) {
	case []any:
		stateNodes = value
	case nil:
	default:
		return nil, errors.New("ledger JSON state nodes must be an array")
	}

	stateMap := shamap.New(shamap.TypeState)
	for i, node := range stateNodes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		object, ok := node.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("accountState[%d] must be an object", i)
		}

		indexValue, exists := object["index"]
		if !exists {
			return nil, fmt.Errorf("accountState[%d] is missing index", i)
		}
		index, err := ledgerFileIndex(indexValue)
		if err != nil {
			return nil, fmt.Errorf("accountState[%d] index: %w", i, err)
		}
		delete(object, "index")

		exists, err = stateMap.HasContext(ctx, index)
		if err != nil {
			return nil, fmt.Errorf("check accountState[%d] index: %w", i, err)
		}
		if exists {
			return nil, fmt.Errorf("accountState[%d] duplicates index %s", i, strings.ToUpper(indexValue.(string)))
		}

		entryType, ok := object["LedgerEntryType"].(string)
		if !ok || entryType == "" {
			return nil, fmt.Errorf("accountState[%d] has invalid LedgerEntryType", i)
		}
		if entryType == "MPTokenIssuance" {
			delete(object, "mpt_issuance_id")
		}

		typed := ledgerfields.New(entryType)
		if typed == nil {
			return nil, fmt.Errorf("accountState[%d] has unknown LedgerEntryType %q", i, entryType)
		}
		blob, err := binarycodec.EncodeBytes(object)
		if err != nil {
			return nil, fmt.Errorf("encode accountState[%d] %s: %w", i, entryType, err)
		}
		if err := typed.Decode(blob); err != nil {
			return nil, fmt.Errorf("validate accountState[%d] %s: %w", i, entryType, err)
		}
		if err := stateMap.Put(index, blob); err != nil {
			return nil, fmt.Errorf("insert accountState[%d] %s: %w", i, entryType, err)
		}
	}

	accountHash, err := stateMap.Hash()
	if err != nil {
		return nil, fmt.Errorf("hash ledger state map: %w", err)
	}
	if accountHash == ([32]byte{}) {
		return nil, errors.New("ledger JSON state map is empty")
	}
	if sequence >= xrpLedgerEarliestFees {
		found, err := stateMap.HasContext(ctx, keylet.Fees().Key)
		if err != nil {
			return nil, fmt.Errorf("read FeeSettings entry: %w", err)
		}
		if !found {
			return nil, fmt.Errorf("ledger %d is missing FeeSettings", sequence)
		}
	}

	rules, err := ledger.LoadAmendmentsFromSHAMapContext(ctx, stateMap)
	if err != nil {
		return nil, fmt.Errorf("load amendment rules: %w", err)
	}
	fees, err := storedLedgerFees(ctx, stateMap, rules.XRPFeesEnabled(), s.configuredFees)
	if err != nil {
		return nil, fmt.Errorf("load ledger fees: %w", err)
	}

	txMap := shamap.New(shamap.TypeTransaction)
	txHash, err := txMap.Hash()
	if err != nil {
		return nil, fmt.Errorf("hash ledger transaction map: %w", err)
	}

	closeFlags := uint8(0)
	if closeTimeEstimated {
		closeFlags = header.LCFNoConsensusTime
	}
	hdr := header.LedgerHeader{
		LedgerIndex:         sequence,
		TxHash:              txHash,
		AccountHash:         accountHash,
		Drops:               totalDrops,
		Accepted:            true,
		CloseFlags:          closeFlags,
		CloseTimeResolution: closeTimeResolution,
		CloseTime:           closeTime,
	}
	hdr.Hash = header.CalculateHash(hdr)

	loaded, err := ledger.NewClosedFromHeaderContext(ctx, hdr, stateMap, txMap, fees)
	if err != nil {
		return nil, fmt.Errorf("construct loaded ledger: %w", err)
	}
	if s.shamapFamily != nil {
		loaded.SetSHAMapFamily(s.shamapFamily)
	}
	return loaded, nil
}

func normalizeLedgerJSONNumbers(value any) (any, error) {
	switch value := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseInt(value.String(), 10, 64)
		if err == nil {
			return parsed, nil
		}
		real, realErr := strconv.ParseFloat(value.String(), 64)
		if realErr != nil {
			return nil, fmt.Errorf("invalid number %q: %w", value, realErr)
		}
		return real, nil
	case []any:
		for i := range value {
			normalized, err := normalizeLedgerJSONNumbers(value[i])
			if err != nil {
				return nil, err
			}
			value[i] = normalized
		}
		return value, nil
	case map[string]any:
		for key, item := range value {
			normalized, err := normalizeLedgerJSONNumbers(item)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			value[key] = normalized
		}
		return value, nil
	default:
		return value, nil
	}
}

func ledgerFileBool(value any) bool {
	switch value := value.(type) {
	case nil:
		return false
	case bool:
		return value
	case int64:
		return value != 0
	case float64:
		return value != 0
	case string:
		return value != ""
	case []any:
		return len(value) != 0
	case map[string]any:
		return len(value) != 0
	default:
		return false
	}
}

func ledgerFileUint32(name string, value any) (uint32, error) {
	var parsed uint64
	switch value := value.(type) {
	case nil:
		return 0, nil
	case bool:
		if value {
			return 1, nil
		}
		return 0, nil
	case string:
		var err error
		parsed, err = strconv.ParseUint(value, 10, 32)
		if err != nil {
			return 0, fmt.Errorf("invalid %s: %w", name, err)
		}
	case int64:
		if value < 0 {
			return 0, fmt.Errorf("%s must be non-negative", name)
		}
		parsed = uint64(value)
	case float64:
		if value < 0 || value > math.MaxUint32 {
			return 0, fmt.Errorf("%s is out of range: %v", name, value)
		}
		return uint32(value), nil
	default:
		return 0, fmt.Errorf("%s must be convertible to an unsigned integer, got %T", name, value)
	}
	if parsed > math.MaxUint32 {
		return 0, fmt.Errorf("%s is out of range: %d", name, parsed)
	}
	return uint32(parsed), nil
}

func ledgerFileUint64(name string, value any) (uint64, error) {
	return ledgerFileUnsigned(name, value, math.MaxUint64)
}

func ledgerFileUnsigned(name string, value any, maximum uint64) (uint64, error) {
	var (
		parsed uint64
		err    error
	)
	switch value := value.(type) {
	case string:
		parsed, err = strconv.ParseUint(value, 10, 64)
	case int64:
		if value < 0 {
			return 0, fmt.Errorf("%s must be non-negative", name)
		}
		parsed = uint64(value)
	default:
		return 0, fmt.Errorf("%s must be an integer or decimal string, got %T", name, value)
	}
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", name, err)
	}
	if parsed > maximum {
		return 0, fmt.Errorf("%s is out of range: %d", name, parsed)
	}
	return parsed, nil
}

func ledgerFileIndex(value any) ([32]byte, error) {
	text, ok := value.(string)
	if !ok {
		return [32]byte{}, fmt.Errorf("must be a hexadecimal string, got %T", value)
	}
	if len(text) != 64 {
		return [32]byte{}, fmt.Errorf("must contain exactly 64 hexadecimal digits")
	}
	decoded, err := hex.DecodeString(text)
	if err != nil {
		return [32]byte{}, fmt.Errorf("invalid hexadecimal value: %w", err)
	}
	var index [32]byte
	copy(index[:], decoded)
	if index == ([32]byte{}) {
		return [32]byte{}, errors.New("must not be zero")
	}
	return index, nil
}
