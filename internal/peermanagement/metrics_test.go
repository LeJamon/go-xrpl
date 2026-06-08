package peermanagement

import (
	"testing"
)

func TestTrafficCounterBasic(t *testing.T) {
	tc := NewTrafficCounter()

	// Add some inbound traffic
	tc.AddCount(CategoryTransaction, true, 100)
	tc.AddCount(CategoryTransaction, true, 200)

	// Add some outbound traffic
	tc.AddCount(CategoryTransaction, false, 150)

	stats := tc.GetStats(CategoryTransaction)
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
		msgType  uint16
		expected TrafficCategory
	}{
		{3, CategoryBase},         // TypePing
		{5, CategoryCluster},      // TypeCluster
		{15, CategoryOverlay},     // TypeEndpoints
		{2, CategoryManifests},    // TypeManifests
		{30, CategoryTransaction}, // TypeTransaction
		{41, CategoryValidation},  // TypeValidation
		{55, CategorySquelch},     // TypeSquelch
		{999, CategoryUnknown},    // Unknown
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

	stats := tc.GetStats(CategoryTransaction)
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
	total := tc.GetTotalStats()
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

func TestPeerScoreBasic(t *testing.T) {
	ps := NewPeerScore()

	// Initial score should be 0
	if ps.Score() != 0 {
		t.Errorf("Expected initial score 0, got %d", ps.Score())
	}
}

func TestPeerScoreValidMessages(t *testing.T) {
	ps := NewPeerScore()

	// Record valid messages
	for range 10 {
		ps.RecordValidMessage()
	}

	// Score should be positive
	if ps.Score() <= 0 {
		t.Errorf("Score should be positive with valid messages, got %d", ps.Score())
	}

	if ps.ValidMessages != 10 {
		t.Errorf("Expected 10 valid messages, got %d", ps.ValidMessages)
	}
}

func TestPeerScoreInvalidMessages(t *testing.T) {
	ps := NewPeerScore()

	// Record invalid messages
	for range 5 {
		ps.RecordInvalidMessage()
	}

	// Score should be negative (invalid messages have 10x weight)
	if ps.Score() >= 0 {
		t.Errorf("Score should be negative with invalid messages, got %d", ps.Score())
	}

	if ps.InvalidMessages != 5 {
		t.Errorf("Expected 5 invalid messages, got %d", ps.InvalidMessages)
	}
}

func TestPeerScoreTimeouts(t *testing.T) {
	ps := NewPeerScore()

	// Record timeouts
	for range 3 {
		ps.RecordTimeout()
	}

	// Score should be negative
	if ps.Score() >= 0 {
		t.Errorf("Score should be negative with timeouts, got %d", ps.Score())
	}

	if ps.Timeouts != 3 {
		t.Errorf("Expected 3 timeouts, got %d", ps.Timeouts)
	}
}

func TestPeerScoreDisconnects(t *testing.T) {
	ps := NewPeerScore()

	// Record disconnects
	for range 2 {
		ps.RecordDisconnect()
	}

	// Score should be negative
	if ps.Score() >= 0 {
		t.Errorf("Score should be negative with disconnects, got %d", ps.Score())
	}

	if ps.Disconnects != 2 {
		t.Errorf("Expected 2 disconnects, got %d", ps.Disconnects)
	}
}

func TestPeerScoreMixed(t *testing.T) {
	ps := NewPeerScore()

	// Record a mix of positive and negative events
	for range 100 {
		ps.RecordValidMessage()
	}
	for range 5 {
		ps.RecordInvalidMessage()
	}

	// With 100 valid messages (+100) and 5 invalid (-50), net should be positive
	score := ps.Score()
	if score <= 0 {
		t.Errorf("Score should be positive with more good events, got %d", score)
	}
}

func TestPeerScoreFormula(t *testing.T) {
	ps := NewPeerScore()

	// Set known values
	ps.ValidMessages = 20
	ps.MessagesRelayed = 100
	ps.Uptime = 120 // 2 minutes
	ps.InvalidMessages = 2
	ps.Timeouts = 1
	ps.Disconnects = 1

	// Expected: positive = 20 + 10 + 2 = 32
	// Expected: negative = 20 + 5 + 3 = 28
	// Expected: score = 32 - 28 = 4
	expectedPositive := 20 + (100 / 10) + (120 / 60)
	expectedNegative := 2*10 + 1*5 + 1*3
	expectedScore := expectedPositive - expectedNegative

	if ps.Score() != expectedScore {
		t.Errorf("Expected score %d, got %d", expectedScore, ps.Score())
	}
}
