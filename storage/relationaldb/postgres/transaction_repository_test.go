//go:build postgres

package postgres

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

func TestGetTransactionRangeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name        string
		ledgerRange relationaldb.LedgerRange
		ledgers     []uint32
		findStored  bool
		wantSearch  relationaldb.TxSearchResult
		wantError   bool
	}{
		{
			name:        "empty maximum singleton",
			ledgerRange: relationaldb.LedgerRange{Min: math.MaxUint32, Max: math.MaxUint32},
			wantSearch:  relationaldb.TxSearchSome,
		},
		{
			name:        "empty full range",
			ledgerRange: relationaldb.LedgerRange{Min: 0, Max: math.MaxUint32},
			wantSearch:  relationaldb.TxSearchSome,
		},
		{
			name:        "populated maximum singleton",
			ledgerRange: relationaldb.LedgerRange{Min: math.MaxUint32, Max: math.MaxUint32},
			ledgers:     []uint32{math.MaxUint32},
			wantSearch:  relationaldb.TxSearchAll,
		},
		{
			name:        "partially populated full range",
			ledgerRange: relationaldb.LedgerRange{Min: 0, Max: math.MaxUint32},
			ledgers:     []uint32{math.MaxUint32},
			wantSearch:  relationaldb.TxSearchSome,
		},
		{
			name:        "duplicate transactions do not fill a missing ledger",
			ledgerRange: relationaldb.LedgerRange{Min: math.MaxUint32 - 1, Max: math.MaxUint32},
			ledgers:     []uint32{math.MaxUint32, math.MaxUint32},
			wantSearch:  relationaldb.TxSearchSome,
		},
		{
			name:        "complete range ending at maximum",
			ledgerRange: relationaldb.LedgerRange{Min: math.MaxUint32 - 1, Max: math.MaxUint32},
			ledgers:     []uint32{math.MaxUint32 - 1, math.MaxUint32},
			wantSearch:  relationaldb.TxSearchAll,
		},
		{
			name:        "found transaction at maximum",
			ledgerRange: relationaldb.LedgerRange{Min: math.MaxUint32, Max: math.MaxUint32},
			ledgers:     []uint32{math.MaxUint32},
			findStored:  true,
			wantSearch:  relationaldb.TxSearchAll,
		},
		{
			name:        "adjacent inverted range",
			ledgerRange: relationaldb.LedgerRange{Min: math.MaxUint32, Max: math.MaxUint32 - 1},
			wantSearch:  relationaldb.TxSearchUnknown,
			wantError:   true,
		},
		{
			name:        "inverted full range",
			ledgerRange: relationaldb.LedgerRange{Min: math.MaxUint32, Max: 0},
			wantSearch:  relationaldb.TxSearchUnknown,
			wantError:   true,
		},
		{
			name:        "inverted range with existing transaction",
			ledgerRange: relationaldb.LedgerRange{Min: math.MaxUint32, Max: math.MaxUint32 - 1},
			ledgers:     []uint32{math.MaxUint32},
			findStored:  true,
			wantSearch:  relationaldb.TxSearchUnknown,
			wantError:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			repo := setupTestDB(t).Transaction()
			for i, seq := range tc.ledgers {
				value := makePersistValue(seq).Transactions[0].Transaction
				value.Hash = relationaldb.Hash{byte(i + 1)}
				if err := repo.SaveTransaction(ctx, value); err != nil {
					t.Fatal(err)
				}
			}

			var hash relationaldb.Hash
			if tc.findStored {
				hash[0] = 1
			}
			got, searched, err := repo.GetTransaction(ctx, hash, &tc.ledgerRange)
			if tc.wantError {
				if !errors.Is(err, relationaldb.ErrInvalidData) || !strings.Contains(err.Error(), "minimum ledger exceeds maximum") {
					t.Fatalf("error = %v, want invalid range error", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if searched != tc.wantSearch {
				t.Errorf("searched = %v, want %v", searched, tc.wantSearch)
			}
			if tc.findStored && !tc.wantError {
				if got == nil || got.Hash != hash || got.LedgerSeq != math.MaxUint32 {
					t.Fatalf("transaction = %+v, want stored transaction at maximum ledger", got)
				}
			} else if got != nil {
				t.Fatalf("transaction = %+v, want nil", got)
			}
		})
	}
}
