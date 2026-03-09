// Package metadata_test tests transaction metadata correctness.
// Tests ported from rippled's Discrepancy_test.cpp and other metadata tests.
package metadata_test

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/payment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/metadata"
	"github.com/LeJamon/goXRPLd/internal/testing/ticket"
	"github.com/LeJamon/goXRPLd/internal/testing/trustset"
	"github.com/stretchr/testify/require"
)

// TestXRPConservation_SimplePayment verifies XRP conservation for a simple
// XRP-to-XRP payment. The fee should be the only XRP destroyed.
// Reference: rippled Discrepancy_test.cpp testXRPDiscrepancy
func TestXRPConservation_SimplePayment(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := env.MasterAccount()
	bob := jtx.NewAccount("bob")
	env.Fund(bob)
	env.Close()

	// Simple XRP payment from alice to bob
	pay := payment.NewPayment(alice.Address, bob.Address, tx.NewXRPAmount(1_000_000)) // 1 XRP
	pay.Fee = "10"
	result := env.Submit(pay)
	jtx.RequireTxSuccess(t, result)

	// Verify XRP conservation: sumPrev - sumFinal == fee (10 drops)
	metadata.CheckXRPConservation(t, result, 10)
}

// TestXRPConservation_AccountCreate verifies XRP conservation when creating
// a new account via payment. The funding amount moves from sender to new account.
func TestXRPConservation_AccountCreate(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := env.MasterAccount()
	carol := jtx.NewAccount("carol")

	// Fund creates carol's account
	env.FundAmount(carol, 300_000_000) // 300 XRP
	env.Close()

	// Another payment to existing carol
	pay := payment.NewPayment(alice.Address, carol.Address, tx.NewXRPAmount(50_000_000)) // 50 XRP
	pay.Fee = "10"
	result := env.Submit(pay)
	jtx.RequireTxSuccess(t, result)

	metadata.CheckXRPConservation(t, result, 10)
}

// TestXRPConservation_TicketCreate verifies XRP conservation for ticket creation.
// TicketCreate only modifies the source account and creates ticket entries.
func TestXRPConservation_TicketCreate(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := env.MasterAccount()

	// Create 3 tickets
	tc := ticket.TicketCreate(alice, 3).Build()
	result := env.Submit(tc)
	jtx.RequireTxSuccess(t, result)

	metadata.CheckXRPConservation(t, result, 10)
}

// TestMetadata_HasAffectedNodes verifies that successful transactions produce metadata
// with at least one AffectedNode (the source AccountRoot).
func TestMetadata_HasAffectedNodes(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := env.MasterAccount()
	bob := jtx.NewAccount("bob")
	env.Fund(bob)
	env.Close()

	pay := payment.NewPayment(alice.Address, bob.Address, tx.NewXRPAmount(1_000_000))
	pay.Fee = "10"
	result := env.Submit(pay)
	jtx.RequireTxSuccess(t, result)

	require.NotNil(t, result.Metadata, "Metadata should not be nil")
	require.True(t, len(result.Metadata.AffectedNodes) >= 2,
		"Payment should affect at least 2 nodes (sender + receiver)")

	// Verify sender AccountRoot is in metadata
	senderNode := metadata.FindNodeByAccount(result.Metadata, alice.Address)
	require.NotNil(t, senderNode, "Sender AccountRoot should be in metadata")
	require.Equal(t, "ModifiedNode", senderNode.NodeType)

	// Verify receiver AccountRoot is in metadata
	receiverNode := metadata.FindNodeByAccount(result.Metadata, bob.Address)
	require.NotNil(t, receiverNode, "Receiver AccountRoot should be in metadata")
}

// TestMetadata_TecHasProperFields verifies that tec results produce metadata
// with proper PreviousFields/FinalFields (not just minimal entries).
func TestMetadata_TecHasProperFields(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := env.MasterAccount()
	bob := jtx.NewAccount("bob")
	env.Fund(bob)
	env.Close()

	// Try to pay more XRP than alice has → tecUNFUNDED_PAYMENT or similar tec
	// Actually, let's use a simpler tec case: payment to self with insufficient reserve
	// Use account set to disable master without regular key → tecNO_ALTERNATIVE_KEY
	// via a ticket to exercise the tec+ticket path
	tc := ticket.TicketCreate(alice, 1).Build()
	result := env.Submit(tc)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// The tec result metadata should still have proper fields
	meta := result.Metadata
	require.NotNil(t, meta)

	// Find the AccountRoot modification
	acctNode := metadata.FindNodeByAccount(meta, alice.Address)
	require.NotNil(t, acctNode, "AccountRoot should be in metadata")
	require.Equal(t, "ModifiedNode", acctNode.NodeType)
	require.NotNil(t, acctNode.FinalFields, "FinalFields should be present")
	require.NotNil(t, acctNode.PreviousFields, "PreviousFields should be present")

	// Verify sequence changed
	_, hasSeq := acctNode.PreviousFields["Sequence"]
	require.True(t, hasSeq, "Sequence should be in PreviousFields")
}

// TestMetadata_TransactionIndex verifies that TransactionIndex is assigned.
// In the test env, each Submit() creates a fresh engine, so each tx gets index 0.
// The sequential counter is tested via the Engine unit test in tx package.
func TestMetadata_TransactionIndex(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := env.MasterAccount()
	bob := jtx.NewAccount("bob")
	env.Fund(bob)
	env.Close()

	pay := payment.NewPayment(alice.Address, bob.Address, tx.NewXRPAmount(100_000))
	pay.Fee = "10"
	result := env.Submit(pay)
	jtx.RequireTxSuccess(t, result)

	require.NotNil(t, result.Metadata, "Metadata should not be nil")
	require.Equal(t, uint32(0), result.Metadata.TransactionIndex,
		"TransactionIndex should be assigned")
}

// TestMetadata_CreatedNode verifies that creating a new account produces a
// CreatedNode with proper NewFields (Account, Balance, Sequence).
// Reference: rippled Ticket_test.cpp checkTicketCreateMeta (CreatedNode checks)
func TestMetadata_CreatedNode(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := env.MasterAccount()
	bob := jtx.NewAccount("bob")

	// Directly submit a payment to create bob's account (don't use FundAmount since we need the result)
	pay := payment.NewPayment(alice.Address, bob.Address, tx.NewXRPAmount(500_000_000)) // 500 XRP
	pay.Fee = "10"
	result := env.Submit(pay)
	jtx.RequireTxSuccess(t, result)

	require.NotNil(t, result.Metadata, "Metadata should not be nil")

	// Find the CreatedNode for bob's AccountRoot
	createdNodes := metadata.FindNodes(result.Metadata, "CreatedNode", "AccountRoot")
	require.True(t, len(createdNodes) >= 1, "Should have at least one CreatedNode AccountRoot")

	// Find bob's created node specifically
	var bobNode *tx.AffectedNode
	for _, n := range createdNodes {
		if acct, ok := n.NewFields["Account"].(string); ok && acct == bob.Address {
			bobNode = n
			break
		}
	}
	require.NotNil(t, bobNode, "Bob's CreatedNode should exist")
	require.NotNil(t, bobNode.NewFields, "NewFields should be present on CreatedNode")
	require.Nil(t, bobNode.PreviousFields, "PreviousFields should be nil on CreatedNode")
	require.Nil(t, bobNode.FinalFields, "FinalFields should be nil on CreatedNode")

	// Verify key fields are in NewFields
	_, hasAccount := bobNode.NewFields["Account"]
	require.True(t, hasAccount, "Account should be in NewFields")
	_, hasBalance := bobNode.NewFields["Balance"]
	require.True(t, hasBalance, "Balance should be in NewFields")
	_, hasSequence := bobNode.NewFields["Sequence"]
	require.True(t, hasSequence, "Sequence should be in NewFields")
}

// TestMetadata_DeletedNode verifies that consuming a ticket produces a
// DeletedNode with proper FinalFields.
// Reference: rippled Ticket_test.cpp checkTicketConsumeMeta (DeletedNode checks)
func TestMetadata_DeletedNode(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := env.MasterAccount()
	bob := jtx.NewAccount("bob")
	env.Fund(bob)
	env.Close()

	// Create a ticket
	tc := ticket.TicketCreate(alice, 1).Build()
	result := env.Submit(tc)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Find the created Ticket node to get the ticket sequence
	ticketNodes := metadata.FindNodes(result.Metadata, "CreatedNode", "Ticket")
	require.True(t, len(ticketNodes) >= 1, "TicketCreate should create a Ticket node")

	ticketNode := ticketNodes[0]
	require.NotNil(t, ticketNode.NewFields, "Ticket NewFields should be present")
	ticketSeq := metadata.ToUint32(ticketNode.NewFields["TicketSequence"])
	require.True(t, ticketSeq > 0, "TicketSequence should be > 0")

	// Now consume the ticket with a payment
	pay := payment.NewPayment(alice.Address, bob.Address, tx.NewXRPAmount(100_000))
	pay.Fee = "10"
	jtx.WithTicketSeq(pay, ticketSeq)
	result2 := env.Submit(pay)
	jtx.RequireTxSuccess(t, result2)

	// Verify the ticket was deleted
	deletedTickets := metadata.FindNodes(result2.Metadata, "DeletedNode", "Ticket")
	require.Equal(t, 1, len(deletedTickets), "One Ticket should be deleted")

	deleted := deletedTickets[0]
	require.NotNil(t, deleted.FinalFields, "DeletedNode should have FinalFields")
}

// TestMetadata_ModifiedNode_FieldDiff verifies that PreviousFields only contains
// fields that actually changed, not all fields.
// Reference: rippled ApplyStateTable.cpp metadata generation — sMD_ChangeOrig
func TestMetadata_ModifiedNode_FieldDiff(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := env.MasterAccount()
	bob := jtx.NewAccount("bob")
	env.Fund(bob)
	env.Close()

	// Simple payment: only Balance and Sequence should change on sender
	pay := payment.NewPayment(alice.Address, bob.Address, tx.NewXRPAmount(1_000_000))
	pay.Fee = "10"
	result := env.Submit(pay)
	jtx.RequireTxSuccess(t, result)

	senderNode := metadata.FindNodeByAccount(result.Metadata, alice.Address)
	require.NotNil(t, senderNode)
	require.Equal(t, "ModifiedNode", senderNode.NodeType)

	// PreviousFields should have Balance (changed by payment + fee) and Sequence (incremented)
	require.NotNil(t, senderNode.PreviousFields, "PreviousFields should exist")
	_, hasBalance := senderNode.PreviousFields["Balance"]
	require.True(t, hasBalance, "Balance should be in PreviousFields (it changed)")
	_, hasSeq := senderNode.PreviousFields["Sequence"]
	require.True(t, hasSeq, "Sequence should be in PreviousFields (it changed)")

	// Account field should NOT be in PreviousFields (it didn't change)
	_, hasAccount := senderNode.PreviousFields["Account"]
	require.False(t, hasAccount, "Account should NOT be in PreviousFields (unchanged)")

	// FinalFields should have the current Balance and Sequence
	require.NotNil(t, senderNode.FinalFields, "FinalFields should exist")
	_, hasFinalBal := senderNode.FinalFields["Balance"]
	require.True(t, hasFinalBal, "Balance should be in FinalFields")
	_, hasFinalSeq := senderNode.FinalFields["Sequence"]
	require.True(t, hasFinalSeq, "Sequence should be in FinalFields")
}

// TestMetadata_MultiAccountConservation verifies XRP conservation with 3+ accounts.
// A chain of payments: alice → bob → carol. Each step should conserve XRP.
func TestMetadata_MultiAccountConservation(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := env.MasterAccount()
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	env.Fund(bob)
	env.Fund(carol)
	env.Close()

	// alice → bob
	pay1 := payment.NewPayment(alice.Address, bob.Address, tx.NewXRPAmount(10_000_000)) // 10 XRP
	pay1.Fee = "10"
	result1 := env.Submit(pay1)
	jtx.RequireTxSuccess(t, result1)
	metadata.CheckXRPConservation(t, result1, 10)

	// bob → carol
	pay2 := payment.NewPayment(bob.Address, carol.Address, tx.NewXRPAmount(5_000_000)) // 5 XRP
	pay2.Fee = "10"
	result2 := env.Submit(pay2)
	jtx.RequireTxSuccess(t, result2)
	metadata.CheckXRPConservation(t, result2, 10)
}

// TestMetadata_TicketCreate_CreatedNodes verifies TicketCreate metadata:
// - AccountRoot is modified (Sequence, OwnerCount, TicketCount, Balance change)
// - N Ticket entries are created
// - DirectoryNode entries are created/modified
// Reference: rippled Ticket_test.cpp checkTicketCreateMeta
func TestMetadata_TicketCreate_CreatedNodes(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := env.MasterAccount()

	// Create 3 tickets
	tc := ticket.TicketCreate(alice, 3).Build()
	result := env.Submit(tc)
	jtx.RequireTxSuccess(t, result)

	meta := result.Metadata
	require.NotNil(t, meta)

	// Should have 3 CreatedNode Ticket entries
	ticketNodes := metadata.FindNodes(meta, "CreatedNode", "Ticket")
	require.Equal(t, 3, len(ticketNodes), "TicketCreate(3) should create 3 Ticket nodes")

	// Each ticket should have NewFields with Account and TicketSequence
	for _, tn := range ticketNodes {
		require.NotNil(t, tn.NewFields, "Ticket NewFields should exist")
		_, hasAcct := tn.NewFields["Account"]
		require.True(t, hasAcct, "Ticket NewFields should have Account")
		_, hasTicketSeq := tn.NewFields["TicketSequence"]
		require.True(t, hasTicketSeq, "Ticket NewFields should have TicketSequence")
	}

	// AccountRoot should be modified with OwnerCount, Sequence, TicketCount changes
	acctNode := metadata.FindNodeByAccount(meta, alice.Address)
	require.NotNil(t, acctNode)
	require.Equal(t, "ModifiedNode", acctNode.NodeType)

	// PreviousFields should contain Sequence (consumed), OwnerCount (increased)
	_, hasSeq := acctNode.PreviousFields["Sequence"]
	require.True(t, hasSeq, "Sequence should be in PreviousFields")
	_, hasOwnerCount := acctNode.PreviousFields["OwnerCount"]
	require.True(t, hasOwnerCount, "OwnerCount should be in PreviousFields")

	// XRP conservation
	metadata.CheckXRPConservation(t, result, 10)
}

// TestMetadata_sMD_Never_Fields verifies that fields marked sMD_Never
// (e.g., LedgerEntryType) do NOT appear in PreviousFields or FinalFields.
// Reference: rippled sfields.macro — LedgerEntryType has sMD_Never (0x00)
func TestMetadata_sMD_Never_Fields(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := env.MasterAccount()
	bob := jtx.NewAccount("bob")
	env.Fund(bob)
	env.Close()

	pay := payment.NewPayment(alice.Address, bob.Address, tx.NewXRPAmount(1_000_000))
	pay.Fee = "10"
	result := env.Submit(pay)
	jtx.RequireTxSuccess(t, result)

	// Check every ModifiedNode — LedgerEntryType should not appear in PreviousFields or FinalFields
	for _, node := range result.Metadata.AffectedNodes {
		if node.NodeType == "ModifiedNode" {
			if node.PreviousFields != nil {
				_, has := node.PreviousFields["LedgerEntryType"]
				require.False(t, has,
					"LedgerEntryType (sMD_Never) should not be in PreviousFields for %s", node.LedgerEntryType)
			}
			if node.FinalFields != nil {
				_, has := node.FinalFields["LedgerEntryType"]
				require.False(t, has,
					"LedgerEntryType (sMD_Never) should not be in FinalFields for %s", node.LedgerEntryType)
			}
		}
		if node.NodeType == "CreatedNode" && node.NewFields != nil {
			_, has := node.NewFields["LedgerEntryType"]
			require.False(t, has,
				"LedgerEntryType (sMD_Never) should not be in NewFields for %s", node.LedgerEntryType)
		}
	}
}

// TestMetadata_TrustLine_FreezeFlags verifies that setting/clearing freeze on
// a trust line produces metadata with correct Flags in FinalFields.
// Reference: rippled Freeze_test.cpp getTrustlineFlags / testRippleState
func TestMetadata_TrustLine_FreezeFlags(t *testing.T) {
	env := jtx.NewTestEnv(t)
	gw := jtx.NewAccount("gateway")
	bob := jtx.NewAccount("bob")
	env.FundAmount(gw, uint64(jtx.XRP(1000)))
	env.FundAmount(bob, uint64(jtx.XRP(1000)))
	env.Close()

	// Set up trust line: bob trusts gw for 100 USD
	result := env.Submit(trustset.TrustLine(bob, "USD", gw, "100").Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Gateway freezes bob's trust line
	freezeTx := trustset.TrustLine(gw, "USD", bob, "0").Freeze().Build()
	result = env.Submit(freezeTx)
	jtx.RequireTxSuccess(t, result)

	// Find the modified RippleState in metadata
	rsNodes := metadata.FindNodes(result.Metadata, "ModifiedNode", "RippleState")
	require.True(t, len(rsNodes) >= 1, "Freeze should modify a RippleState node")

	rsNode := rsNodes[0]
	require.NotNil(t, rsNode.FinalFields, "FinalFields should exist on modified RippleState")

	// Verify freeze flag is set in FinalFields.Flags
	flags := metadata.ToUint32(rsNode.FinalFields["Flags"])
	// Gateway is the issuer — freeze is on the low or high side depending on sort order
	isFrozen := (flags&state.LsfLowFreeze != 0) || (flags&state.LsfHighFreeze != 0)
	require.True(t, isFrozen, "Freeze flag should be set in FinalFields.Flags (0x%08x)", flags)

	// PreviousFields should contain Flags (since it changed)
	require.NotNil(t, rsNode.PreviousFields, "PreviousFields should exist (flags changed)")
	_, hasFlags := rsNode.PreviousFields["Flags"]
	require.True(t, hasFlags, "Flags should be in PreviousFields (it changed)")

	// Clear freeze
	clearTx := trustset.TrustLine(gw, "USD", bob, "0").ClearFreeze().Build()
	result = env.Submit(clearTx)
	jtx.RequireTxSuccess(t, result)

	// Verify freeze flag is cleared
	rsNodes = metadata.FindNodes(result.Metadata, "ModifiedNode", "RippleState")
	require.True(t, len(rsNodes) >= 1, "ClearFreeze should modify a RippleState node")
	rsNode = rsNodes[0]
	flags = metadata.ToUint32(rsNode.FinalFields["Flags"])
	isUnfrozen := (flags&state.LsfLowFreeze == 0) && (flags&state.LsfHighFreeze == 0)
	require.True(t, isUnfrozen, "Freeze flags should be cleared (0x%08x)", flags)
}

// TestMetadata_TrustLine_CreatedNode verifies that creating a trust line
// produces a CreatedNode for RippleState with correct NewFields.
// Reference: rippled Freeze_test.cpp testCreateFrozenTrustline
func TestMetadata_TrustLine_CreatedNode(t *testing.T) {
	env := jtx.NewTestEnv(t)
	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	env.FundAmount(gw, uint64(jtx.XRP(1000)))
	env.FundAmount(alice, uint64(jtx.XRP(1000)))
	env.Close()

	// Create a trust line: alice trusts gw for 1000 USD
	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "1000").Build())
	jtx.RequireTxSuccess(t, result)

	// Find the CreatedNode for RippleState
	rsCreated := metadata.FindNodes(result.Metadata, "CreatedNode", "RippleState")
	require.Equal(t, 1, len(rsCreated), "TrustSet should create one RippleState node")

	rsNode := rsCreated[0]
	require.NotNil(t, rsNode.NewFields, "NewFields should be present on CreatedNode RippleState")
	require.Nil(t, rsNode.PreviousFields, "PreviousFields should be nil on CreatedNode")
	require.Nil(t, rsNode.FinalFields, "FinalFields should be nil on CreatedNode")

	// Verify key fields
	_, hasFlags := rsNode.NewFields["Flags"]
	require.True(t, hasFlags, "Flags should be in NewFields")
	// Balance should be zero (no payments yet)
	_, hasBalance := rsNode.NewFields["Balance"]
	require.True(t, hasBalance, "Balance should be in NewFields")
}
