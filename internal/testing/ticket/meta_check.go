// Package ticket provides test helpers for Ticket transaction testing.
package ticket

import (
	"sort"
	"strings"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/tx"
	tickettx "github.com/LeJamon/goXRPLd/internal/tx/ticket"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/stretchr/testify/require"
)

// CheckTicketCreateMeta validates metadata for a successful TicketCreate transaction.
// Ported from rippled's Ticket_test.cpp checkTicketCreateMeta() (lines 35-248).
//
// It verifies:
//   - AccountRoot sequence advanced correctly (by count+1 for seq, by count for ticket)
//   - OwnerCount incremented by (count - consumedTickets)
//   - TicketCount updated correctly
//   - Exactly `count` Ticket nodes were created with sequential TicketSequence values
//   - If a ticket was consumed (txSeq==0), exactly one Ticket node was deleted
//   - At least one DirectoryNode was modified or created
func CheckTicketCreateMeta(
	t *testing.T,
	result jtx.TxResult,
	txn tx.Transaction,
) {
	t.Helper()

	common := txn.GetCommon()
	require.Equal(t, "TicketCreate", txn.TxType().String(), "Expected TicketCreate transaction")

	meta := result.Metadata
	require.NotNil(t, meta, "Metadata should not be nil for applied transaction")
	require.True(t,
		meta.TransactionResult.String() == "tesSUCCESS",
		"Not metadata for successful TicketCreate, got: "+meta.TransactionResult.String())

	// Extract transaction fields
	tc, ok := txn.(*tickettx.TicketCreate)
	require.True(t, ok, "Transaction must be *tickettx.TicketCreate")
	count := tc.TicketCount
	require.True(t, count >= 1, "TicketCount must be >= 1")

	var txSeq uint32
	if common.Sequence != nil {
		txSeq = *common.Sequence
	}
	account := common.Account

	directoryChanged := false
	var acctRootFinalSeq uint32
	var ticketSeqs []uint32
	var ticketsDeleted int

	for _, node := range meta.AffectedNodes {
		switch node.NodeType {
		case "ModifiedNode":
			switch node.LedgerEntryType {
			case "AccountRoot":
				prevFields := node.PreviousFields
				finalFields := node.FinalFields
				require.NotNil(t, prevFields, "ModifiedNode AccountRoot should have PreviousFields")
				require.NotNil(t, finalFields, "ModifiedNode AccountRoot should have FinalFields")

				// Verify the account root Sequence did the right thing
				prevSeq := toUint32(prevFields["Sequence"])
				acctRootFinalSeq = toUint32(finalFields["Sequence"])

				if txSeq == 0 {
					// Transaction used a TicketSequence
					require.Equal(t, prevSeq+count, acctRootFinalSeq,
						"Final sequence should be prevSeq + count when using ticket")
				} else {
					// Transaction used a regular Sequence
					require.Equal(t, txSeq, prevSeq,
						"Previous sequence should equal transaction sequence")
					require.Equal(t, prevSeq+count+1, acctRootFinalSeq,
						"Final sequence should be prevSeq + count + 1")
				}

				consumedTickets := uint32(0)
				if txSeq == 0 {
					consumedTickets = 1
				}

				// If count==1 and a ticket was consumed, net change is 0 so
				// previous OwnerCount/TicketCount are not reported.
				unreportedPrevTicketCount := count == 1 && txSeq == 0

				// Verify OwnerCount
				if unreportedPrevTicketCount {
					_, hasPrevOwnerCount := prevFields["OwnerCount"]
					require.False(t, hasPrevOwnerCount,
						"OwnerCount should not be in PreviousFields when count didn't change")
				} else {
					prevCount := toUint32(prevFields["OwnerCount"])
					finalCount := toUint32(finalFields["OwnerCount"])
					require.Equal(t, prevCount+count-consumedTickets, finalCount,
						"OwnerCount should increase by count - consumedTickets")
				}

				// Verify TicketCount
				_, hasTicketCountFinal := finalFields["TicketCount"]
				require.True(t, hasTicketCountFinal,
					"FinalFields should contain TicketCount")

				if unreportedPrevTicketCount {
					_, hasPrevTicketCount := prevFields["TicketCount"]
					require.False(t, hasPrevTicketCount,
						"TicketCount should not be in PreviousFields when count didn't change")
				} else {
					var startCount uint32
					if v, ok := prevFields["TicketCount"]; ok {
						startCount = toUint32(v)
					}
					// If startCount == 0, TicketCount should NOT be in PreviousFields
					_, hasPrevTicketCount := prevFields["TicketCount"]
					require.Equal(t, startCount == 0, !hasPrevTicketCount,
						"TicketCount presence in PreviousFields should match whether it was previously > 0")

					require.Equal(t,
						startCount+count-consumedTickets,
						toUint32(finalFields["TicketCount"]),
						"Final TicketCount should be startCount + count - consumed")
				}

			case "DirectoryNode":
				directoryChanged = true

			default:
				t.Fatalf("Unexpected modified node type: %s", node.LedgerEntryType)
			}

		case "CreatedNode":
			switch node.LedgerEntryType {
			case "Ticket":
				require.NotNil(t, node.NewFields, "CreatedNode Ticket should have NewFields")
				ticketAccount, _ := node.NewFields["Account"].(string)
				require.Equal(t, account, ticketAccount,
					"Created ticket account should match transaction account")
				ticketSeqs = append(ticketSeqs, toUint32(node.NewFields["TicketSequence"]))

			case "DirectoryNode":
				directoryChanged = true

			default:
				t.Fatalf("Unexpected created node type: %s", node.LedgerEntryType)
			}

		case "DeletedNode":
			if node.LedgerEntryType == "Ticket" {
				// A ticket was consumed — verify txSeq == 0
				require.Equal(t, uint32(0), txSeq,
					"Deleted ticket should only appear when using TicketSequence (Seq==0)")

				require.NotNil(t, node.FinalFields, "DeletedNode Ticket should have FinalFields")
				deletedAccount, _ := node.FinalFields["Account"].(string)
				require.Equal(t, account, deletedAccount,
					"Deleted ticket account should match transaction account")

				deletedTicketSeq := toUint32(node.FinalFields["TicketSequence"])
				require.NotNil(t, common.TicketSequence)
				require.Equal(t, *common.TicketSequence, deletedTicketSeq,
					"Deleted ticket TicketSequence should match transaction TicketSequence")
				ticketsDeleted++
			}
		}
	}

	require.True(t, directoryChanged, "At least one DirectoryNode should be modified or created")

	// Verify all expected tickets were created
	require.Equal(t, int(count), len(ticketSeqs),
		"Should have created exactly count tickets")

	sort.Slice(ticketSeqs, func(i, j int) bool { return ticketSeqs[i] < ticketSeqs[j] })

	// Verify no duplicates
	for i := 1; i < len(ticketSeqs); i++ {
		require.NotEqual(t, ticketSeqs[i-1], ticketSeqs[i],
			"Ticket sequences should be unique")
	}

	// Last ticket sequence should be acctRootFinalSeq - 1
	require.Equal(t, acctRootFinalSeq-1, ticketSeqs[len(ticketSeqs)-1],
		"Last ticket sequence should be final account sequence - 1")

	// If a ticket was consumed, exactly one should have been deleted
	if txSeq == 0 {
		require.Equal(t, 1, ticketsDeleted,
			"Exactly one ticket should be deleted when consuming a ticket")
	}
}

// CheckTicketConsumeMeta validates metadata for a transaction that consumes a ticket.
// The transaction may have succeeded (tesSUCCESS) or failed with a tec code.
// Ported from rippled's Ticket_test.cpp checkTicketConsumeMeta() (lines 256-380).
//
// It verifies:
//   - Transaction Sequence == 0 and TicketSequence is set
//   - Result is tesSUCCESS or tec*
//   - AccountRoot TicketCount decremented by 1 (removed if was 1)
//   - Exactly one Ticket node was deleted with matching Account and TicketSequence
//   - Consumed ticket sequence < final account sequence
func CheckTicketConsumeMeta(
	t *testing.T,
	result jtx.TxResult,
	txn tx.Transaction,
) {
	t.Helper()

	common := txn.GetCommon()

	// Verify Sequence == 0
	require.NotNil(t, common.Sequence, "Transaction should have Sequence set")
	require.Equal(t, uint32(0), *common.Sequence,
		"Transaction Sequence must be 0 for ticket-consuming transactions")

	// Verify TicketSequence is set
	require.NotNil(t, common.TicketSequence,
		"Transaction must have TicketSequence for a ticket-consuming transaction")
	ticketSeq := *common.TicketSequence
	account := common.Account

	meta := result.Metadata
	require.NotNil(t, meta, "Metadata should not be nil for applied transaction")

	// Result must be tesSUCCESS or tec*
	resultStr := meta.TransactionResult.String()
	require.True(t,
		resultStr == "tesSUCCESS" || strings.HasPrefix(resultStr, "tec"),
		"Metadata result should be tesSUCCESS or tec*, got: "+resultStr)

	acctRootFound := false
	var acctRootSeq uint32
	ticketsRemoved := 0

	for _, node := range meta.AffectedNodes {
		switch node.NodeType {
		case "ModifiedNode":
			if node.LedgerEntryType == "AccountRoot" {
				finalFields := node.FinalFields
				if finalFields == nil {
					continue
				}
				// Check if this is the transaction's account
				acctAddr, _ := finalFields["Account"].(string)
				if acctAddr != account {
					continue
				}

				acctRootFound = true
				acctRootSeq = toUint32(finalFields["Sequence"])

				prevFields := node.PreviousFields
				require.NotNil(t, prevFields, "AccountRoot previous fields must be present")

				// TicketCount must be in PreviousFields
				prevTicketCountVal, hasPrevTicketCount := prevFields["TicketCount"]
				require.True(t, hasPrevTicketCount,
					"AccountRoot PreviousFields must contain TicketCount")

				prevTicketCount := toUint32(prevTicketCountVal)
				require.True(t, prevTicketCount > 0,
					"Previous TicketCount must be > 0")

				if prevTicketCount == 1 {
					// TicketCount field should be removed from FinalFields
					_, hasFinalTicketCount := finalFields["TicketCount"]
					require.False(t, hasFinalTicketCount,
						"TicketCount should be absent from FinalFields when decremented to 0")
				} else {
					finalTicketCount := toUint32(finalFields["TicketCount"])
					require.Equal(t, prevTicketCount-1, finalTicketCount,
						"Final TicketCount should be previous - 1")
				}
			}

		case "DeletedNode":
			if node.LedgerEntryType == "Ticket" {
				require.NotNil(t, node.FinalFields, "DeletedNode Ticket should have FinalFields")

				deletedAccount, _ := node.FinalFields["Account"].(string)
				require.Equal(t, account, deletedAccount,
					"Deleted ticket account should match transaction account")

				deletedTicketSeq := toUint32(node.FinalFields["TicketSequence"])
				require.Equal(t, ticketSeq, deletedTicketSeq,
					"Deleted ticket TicketSequence should match transaction TicketSequence")

				ticketsRemoved++
			}
		}
	}

	require.True(t, acctRootFound, "AccountRoot modification must be found")
	require.Equal(t, 1, ticketsRemoved, "Exactly one ticket should be deleted")
	require.True(t, ticketSeq < acctRootSeq,
		"Consumed ticket sequence should be less than final account sequence")
}

// toUint32 converts various numeric types to uint32.
func toUint32(v any) uint32 {
	switch val := v.(type) {
	case uint32:
		return val
	case float64:
		return uint32(val)
	case int:
		return uint32(val)
	case int64:
		return uint32(val)
	case uint64:
		return uint32(val)
	default:
		return 0
	}
}
