package peermanagement

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

func TestTrafficCounterBasic(t *testing.T) {
	tc := NewTrafficCounter()

	// Add some inbound traffic
	tc.AddCount(CategoryTransaction, true, 100)
	tc.AddCount(CategoryTransaction, true, 200)

	// Add some outbound traffic
	tc.AddCount(CategoryTransaction, false, 150)

	stats := tc.Stats(CategoryTransaction)
	if stats == nil {
		t.Fatal("Stats should not be nil")
	}

	if stats.BytesIn != 300 {
		t.Errorf("Expected BytesIn 300, got %d", stats.BytesIn)
	}

	if stats.BytesOut != 150 {
		t.Errorf("Expected BytesOut 150, got %d", stats.BytesOut)
	}

	if stats.MessagesIn != 2 {
		t.Errorf("Expected MessagesIn 2, got %d", stats.MessagesIn)
	}

	if stats.MessagesOut != 1 {
		t.Errorf("Expected MessagesOut 1, got %d", stats.MessagesOut)
	}
}

func TestTrafficCounterCategorize(t *testing.T) {
	tests := []struct {
		msgType  message.MessageType
		expected TrafficCategory
	}{
		{message.TypePing, CategoryBase},
		{message.TypeCluster, CategoryCluster},
		{message.TypeEndpoints, CategoryOverlay},
		{message.TypeManifests, CategoryManifests},
		{message.TypeTransaction, CategoryTransaction},
		{message.TypeValidation, CategoryValidation},
		{message.TypeSquelch, CategorySquelch},
		{message.MessageType(999), CategoryUnknown},
	}

	for _, tc := range tests {
		result := CategorizeMessage(tc.msgType)
		if result != tc.expected {
			t.Errorf("CategorizeMessage(%d) = %v, expected %v", tc.msgType, result, tc.expected)
		}
	}
}

func TestTrafficCounterReset(t *testing.T) {
	tc := NewTrafficCounter()

	tc.AddCount(CategoryTransaction, true, 100)
	tc.Reset()

	stats := tc.Stats(CategoryTransaction)
	if stats.BytesIn != 0 {
		t.Errorf("Expected BytesIn 0 after reset, got %d", stats.BytesIn)
	}
}

func TestTrafficCategoryString(t *testing.T) {
	tests := []struct {
		cat      TrafficCategory
		expected string
	}{
		{CategoryBase, "overhead"},
		{CategoryTransaction, "transactions"},
		{CategoryValidation, "validations"},
		{CategoryTotal, "total"},
		{CategoryUnknown, "unknown"},
	}

	for _, tc := range tests {
		if tc.cat.String() != tc.expected {
			t.Errorf("Category(%d).String() = %s, expected %s", tc.cat, tc.cat.String(), tc.expected)
		}
	}
}

func TestTrafficCounterTotalStats(t *testing.T) {
	tc := NewTrafficCounter()

	// Add traffic to different categories
	tc.AddCount(CategoryTransaction, true, 100)
	tc.AddCount(CategoryValidation, true, 50)
	tc.AddCount(CategoryTransaction, false, 75)

	// Total should aggregate all categories
	total := tc.TotalStats()
	if total == nil {
		t.Fatal("Total stats should not be nil")
	}

	if total.BytesIn != 150 {
		t.Errorf("Expected total BytesIn 150, got %d", total.BytesIn)
	}

	if total.BytesOut != 75 {
		t.Errorf("Expected total BytesOut 75, got %d", total.BytesOut)
	}

	if total.MessagesIn != 2 {
		t.Errorf("Expected total MessagesIn 2, got %d", total.MessagesIn)
	}
}
