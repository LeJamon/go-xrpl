package tx

import "testing"

func TestCommonSeqProxy(t *testing.T) {
	nonzeroSequence := uint32(7)
	zeroSequence := uint32(0)
	ticketSequence := uint32(11)

	tests := []struct {
		name     string
		common   Common
		expected uint32
	}{
		{name: "sequence", common: Common{Sequence: &nonzeroSequence}, expected: nonzeroSequence},
		{name: "ticket", common: Common{TicketSequence: &ticketSequence}, expected: ticketSequence},
		{name: "zero sequence with ticket", common: Common{Sequence: &zeroSequence, TicketSequence: &ticketSequence}, expected: ticketSequence},
		{name: "nonzero sequence with ticket", common: Common{Sequence: &nonzeroSequence, TicketSequence: &ticketSequence}, expected: nonzeroSequence},
		{name: "zero sequence", common: Common{Sequence: &zeroSequence}, expected: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.common.SeqProxy(); got != test.expected {
				t.Fatalf("SeqProxy() = %d, want %d", got, test.expected)
			}
		})
	}
}
