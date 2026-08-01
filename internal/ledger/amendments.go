// Copyright (c) 2024-2025. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ledger

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/keylet"
	ledgerentry "github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/shamap"
)

// LoadAmendmentsFromLedger reads the Amendments ledger entry into a Rules set.
func LoadAmendmentsFromLedger(reader Reader) (*amendment.Rules, error) {
	amendmentsKey := keylet.Amendments()

	exists, err := reader.Exists(amendmentsKey)
	if err != nil {
		return nil, fmt.Errorf("failed to check amendments existence: %w", err)
	}
	if !exists {
		return newRulesWithPermanentAmendments(nil), nil
	}

	data, err := reader.Read(amendmentsKey)
	if err != nil {
		return nil, fmt.Errorf("failed to read amendments entry: %w", err)
	}
	return loadAmendmentsFromData(data)
}

func loadAmendmentsFromSHAMap(stateMap *shamap.SHAMap) (*amendment.Rules, error) {
	return LoadAmendmentsFromSHAMapContext(context.Background(), stateMap)
}

func LoadAmendmentsFromSHAMapContext(ctx context.Context, stateMap *shamap.SHAMap) (*amendment.Rules, error) {
	item, found, err := stateMap.GetContext(ctx, keylet.Amendments().Key)
	if err != nil {
		return nil, fmt.Errorf("failed to read amendments entry: %w", err)
	}
	if !found {
		return newRulesWithPermanentAmendments(nil), nil
	}
	return loadAmendmentsFromData(item.Data())
}

func loadAmendmentsFromData(data []byte) (*amendment.Rules, error) {
	var decoded ledgerentry.Amendments
	if err := decoded.Decode(data); err != nil {
		return nil, fmt.Errorf("failed to decode amendments entry: %w", err)
	}

	enabledIDs, err := decodeAmendmentIDs(decoded.Amendments)
	if err != nil {
		return nil, err
	}
	return newRulesWithPermanentAmendments(enabledIDs), nil
}

func decodeAmendmentIDs(values []string) ([][32]byte, error) {
	ids := make([][32]byte, 0, len(values))
	for i, value := range values {
		decoded, err := hex.DecodeString(value)
		if err != nil {
			return nil, fmt.Errorf("failed to decode amendment %d: %w", i, err)
		}
		if len(decoded) != len([32]byte{}) {
			return nil, fmt.Errorf("failed to decode amendment %d: decoded length %d, want 32", i, len(decoded))
		}

		var id [32]byte
		copy(id[:], decoded)
		ids = append(ids, id)
	}
	return ids, nil
}

func newRulesWithPermanentAmendments(enabledIDs [][32]byte) *amendment.Rules {
	permanentIDs := amendment.PermanentlyEnabledIDs()
	ids := make([][32]byte, 0, len(enabledIDs)+len(permanentIDs))
	ids = append(ids, enabledIDs...)
	ids = append(ids, permanentIDs...)
	return amendment.NewRules(ids)
}

// LoadAmendmentsFromLedgerEntry parses raw Amendments ledger entry data directly.
func LoadAmendmentsFromLedgerEntry(data []byte) (*amendment.Rules, error) {
	return loadAmendmentsFromData(data)
}
