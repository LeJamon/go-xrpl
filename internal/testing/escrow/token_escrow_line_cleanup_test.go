package escrow_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	batchtest "github.com/LeJamon/go-xrpl/internal/testing/batch"
	checktest "github.com/LeJamon/go-xrpl/internal/testing/check"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/LeJamon/go-xrpl/internal/testing/metadata"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	sponsortx "github.com/LeJamon/go-xrpl/internal/tx/sponsor"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type lineSide struct {
	reserve, noRipple uint32
}

func holderSide(holderLow bool) lineSide {
	if holderLow {
		return lineSide{reserve: state.LsfLowReserve, noRipple: state.LsfLowNoRipple}
	}
	return lineSide{reserve: state.LsfHighReserve, noRipple: state.LsfHighNoRipple}
}

// newLineCleanupPair funds a holder and an issuer whose account IDs place the
// holder on the requested side of their trust line.
func newLineCleanupPair(t *testing.T, env *jtx.TestEnv, holderLow, holderDefaultRipple bool) (holder, issuer *jtx.Account) {
	t.Helper()
	holder, issuer = jtx.NewAccount("alice"), jtx.NewAccount("gateway")
	if (state.CompareAccountIDs(holder.ID, issuer.ID) < 0) != holderLow {
		holder, issuer = issuer, holder
	}
	fund5000(env, holder, issuer)
	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(issuer).AllowTrustLineLocking().Build()))
	if !holderDefaultRipple {
		jtx.RequireTxSuccess(t, env.Submit(
			accountset.AccountSet(holder).ClearFlag(accounttx.AccountSetFlagDefaultRipple).Build()))
	}
	env.Close()
	return holder, issuer
}

func issuerCheck(t *testing.T, env *jtx.TestEnv, holder, issuer *jtx.Account, amount tx.Amount) string {
	t.Helper()
	seq := env.Seq(issuer)
	jtx.RequireTxSuccess(t, env.Submit(checktest.CheckCreate(issuer, holder, amount).Build()))
	env.Close()
	return checktest.GetCheckID(issuer, seq)
}

// cashIssuerCheck auto-creates the holder's zero-limit default trust line
// carrying amount, the line shape of the ledger 22280293 incident.
func cashIssuerCheck(t *testing.T, env *jtx.TestEnv, holder, issuer *jtx.Account, amount tx.Amount) {
	t.Helper()
	checkID := issuerCheck(t, env, holder, issuer, amount)
	jtx.RequireTxSuccess(t, env.Submit(checktest.CheckCashAmount(holder, checkID, amount).Build()))
	env.Close()
}

func selfIOUEscrow(env *jtx.TestEnv, holder *jtx.Account, amount tx.Amount) *escrow.EscrowCreateBuilder {
	return escrow.EscrowCreate(holder, holder, 0).
		IOUAmount(amount).
		FinishTime(env.Now().Add(time.Second)).
		CancelTime(env.Now().Add(time.Hour))
}

func readTrustLine(t *testing.T, env *jtx.TestEnv, a, b *jtx.Account) *state.RippleState {
	t.Helper()
	data, err := env.LedgerEntry(keylet.Line(a.ID, b.ID, "USD"))
	require.NoError(t, err)
	require.NotNil(t, data, "trust line must exist")
	line, err := state.ParseRippleState(data)
	require.NoError(t, err)
	return line
}

func readAccountRoot(t *testing.T, env *jtx.TestEnv, account *jtx.Account) *state.AccountRoot {
	t.Helper()
	root, err := state.ReadAccountRoot(env.Ledger(), account.ID)
	require.NoError(t, err)
	require.NotNil(t, root)
	return root
}

// requireExhaustedLineDeleted asserts the canonical lock outcome on an empty
// default line: the holder's reserve is released and the line leaves both
// owner directories.
func requireExhaustedLineDeleted(
	t *testing.T,
	env *jtx.TestEnv,
	meta *tx.Metadata,
	holder, issuer *jtx.Account,
	side lineSide,
	holderDefaultRipple bool,
) {
	t.Helper()
	lineKey := keylet.Line(holder.ID, issuer.ID, "USD")
	deleted := metadata.FindNode(meta, "DeletedNode", "RippleState")
	require.NotNil(t, deleted, "exhausted default trust line must be deleted")
	require.Equal(t, fmt.Sprintf("%X", lineKey.Key), deleted.LedgerIndex)
	wantFlags := uint32(0)
	if !holderDefaultRipple {
		wantFlags = side.noRipple
	}
	require.Equal(t, wantFlags, metadata.ToUint32(deleted.FinalFields["Flags"]))

	require.False(t, env.TrustLineExists(holder, issuer, "USD"))
	requireOwnerDirContains(t, env, holder, lineKey.Key, false)
	requireOwnerDirContains(t, env, issuer, lineKey.Key, false)
}

// TestIOUEscrow_CreateDeletesExhaustedDefaultLine escrows a holder's entire
// balance on an auto-created default line: the holder's line reserve must be
// released and the line deleted, leaving only the escrow owned.
func TestIOUEscrow_CreateDeletesExhaustedDefaultLine(t *testing.T) {
	for _, rules := range escrowReserveRuleCases() {
		for _, holderLow := range []bool{false, true} {
			for _, defaultRipple := range []bool{false, true} {
				name := fmt.Sprintf("%s/holderLow=%t/defaultRipple=%t", rules.name, holderLow, defaultRipple)
				t.Run(name, func(t *testing.T) {
					env := newEscrowReserveEnv(t, rules)
					holder, issuer := newLineCleanupPair(t, env, holderLow, defaultRipple)
					side := holderSide(holderLow)
					amount := usd(100, issuer)
					cashIssuerCheck(t, env, holder, issuer, amount)
					require.NotZero(t, env.TrustLineFlags(holder, issuer, "USD")&side.reserve)
					require.Equal(t, uint32(1), env.OwnerCount(holder))
					issuerOwners := env.OwnerCount(issuer)

					seq := env.Seq(holder)
					result := env.Submit(selfIOUEscrow(env, holder, amount).Build())
					jtx.RequireTxSuccess(t, result)
					escrowKey := keylet.Escrow(holder.ID, seq)
					requireEscrowMetaNode(t, result, "CreatedNode", escrowKey)
					env.Close()

					requireExhaustedLineDeleted(t, env, result.Metadata, holder, issuer, side, defaultRipple)
					require.Equal(t, uint32(1), env.OwnerCount(holder))
					require.Equal(t, issuerOwners, env.OwnerCount(issuer))
					requireOwnerDirContains(t, env, holder, escrowKey.Key, true)
					requireOwnerDirContains(t, env, issuer, escrowKey.Key, true)

					jtx.RequireTxSuccess(t, env.Submit(escrow.EscrowFinish(holder, holder, seq).Build()))
					env.Close()

					require.False(t, env.LedgerEntryExists(escrowKey))
					require.Equal(t, amount, *env.IOUBalance(holder, issuer, "USD"))
					require.Equal(t, uint32(1), env.OwnerCount(holder))
					require.Equal(t, issuerOwners, env.OwnerCount(issuer))
				})
			}
		}
	}
}

// TestIOUEscrow_CreateRetainsNonDefaultLine covers lines the lock must keep:
// any remaining balance or non-default holder setting keeps the holder's
// reserve, and an issuer-side reserve keeps the line after the holder's
// reserve is released.
func TestIOUEscrow_CreateRetainsNonDefaultLine(t *testing.T) {
	tests := []struct {
		name               string
		setup              func(t *testing.T, env *jtx.TestEnv, holder, issuer *jtx.Account)
		escrowed           int64
		wantHolderReserve  bool
		wantHolderOwners   uint32
		wantIssuerOwnerAdd uint32
	}{
		{
			name:              "PartialBalance",
			setup:             func(*testing.T, *jtx.TestEnv, *jtx.Account, *jtx.Account) {},
			escrowed:          40,
			wantHolderReserve: true,
			wantHolderOwners:  2,
		},
		{
			name: "NonzeroLimit",
			setup: func(t *testing.T, env *jtx.TestEnv, holder, issuer *jtx.Account) {
				env.Trust(holder, tx.NewIssuedAmountFromFloat64(1000, "USD", issuer.Address))
				env.Close()
			},
			escrowed:          100,
			wantHolderReserve: true,
			wantHolderOwners:  2,
		},
		{
			name: "QualityIn",
			setup: func(t *testing.T, env *jtx.TestEnv, holder, issuer *jtx.Account) {
				jtx.RequireTxSuccess(t, env.Submit(
					trustset.TrustSet(holder, tx.NewIssuedAmountFromFloat64(0, "USD", issuer.Address)).
						QualityIn(1_100_000_000).Build()))
				env.Close()
			},
			escrowed:          100,
			wantHolderReserve: true,
			wantHolderOwners:  2,
		},
		{
			name: "IssuerReserve",
			setup: func(t *testing.T, env *jtx.TestEnv, holder, issuer *jtx.Account) {
				jtx.RequireTxSuccess(t, env.Submit(
					trustset.TrustSet(issuer, tx.NewIssuedAmountFromFloat64(10, "USD", holder.Address)).Build()))
				env.Close()
			},
			escrowed:           100,
			wantHolderOwners:   1,
			wantIssuerOwnerAdd: 1,
		},
	}

	for _, tt := range tests {
		for _, holderLow := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/holderLow=%t", tt.name, holderLow), func(t *testing.T) {
				env := newEscrowReserveEnv(t, escrowReserveRules{sponsor: true, cleanup: true})
				holder, issuer := newLineCleanupPair(t, env, holderLow, false)
				side := holderSide(holderLow)
				issuerOwners := env.OwnerCount(issuer)
				cashIssuerCheck(t, env, holder, issuer, usd(100, issuer))
				tt.setup(t, env, holder, issuer)

				result := env.Submit(selfIOUEscrow(env, holder, usd(tt.escrowed, issuer)).Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()

				require.Nil(t, metadata.FindNode(result.Metadata, "DeletedNode", "RippleState"))
				line := readTrustLine(t, env, holder, issuer)
				require.Equal(t, tt.wantHolderReserve, line.Flags&side.reserve != 0)
				require.Equal(t, usd(100-tt.escrowed, issuer), *env.IOUBalance(holder, issuer, "USD"))
				require.Equal(t, tt.wantHolderOwners, env.OwnerCount(holder))
				require.Equal(t, issuerOwners+tt.wantIssuerOwnerAdd, env.OwnerCount(issuer))
			})
		}
	}
}

// TestIOUEscrow_CreateReleasesSponsoredReserves checks that sponsor counters
// follow both the released line reserve and the newly charged escrow reserve.
func TestIOUEscrow_CreateReleasesSponsoredReserves(t *testing.T) {
	sponsorBudget := func(t *testing.T, env *jtx.TestEnv, sponsor, sponsee *jtx.Account) {
		t.Helper()
		remaining := int32(1)
		set := sponsortx.NewSponsorshipSet(sponsor.Address)
		set.Sponsee = sponsee.Address
		set.RemainingOwnerCountDelta = &remaining
		jtx.RequireTxSuccess(t, env.Submit(set))
		env.Close()
	}
	sponsorReserve := func(common *tx.Common, sponsor *jtx.Account) {
		flags := tx.SpfSponsorReserve
		common.Sponsor = sponsor.Address
		common.SponsorFlags = &flags
	}

	for _, sponsoredObject := range []string{"Escrow", "TrustLine"} {
		for _, holderLow := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/holderLow=%t", sponsoredObject, holderLow), func(t *testing.T) {
				env := newEscrowReserveEnv(t, escrowReserveRules{sponsor: true, cleanup: true})
				holder, issuer := newLineCleanupPair(t, env, holderLow, false)
				side := holderSide(holderLow)
				sponsor := jtx.NewAccount("sponsor")
				fund5000(env, sponsor)
				env.Close()
				sponsorBudget(t, env, sponsor, holder)
				amount := usd(100, issuer)

				if sponsoredObject == "TrustLine" {
					checkID := issuerCheck(t, env, holder, issuer, amount)
					cash := checktest.CheckCashAmount(holder, checkID, amount).Build()
					sponsorReserve(cash.GetCommon(), sponsor)
					jtx.RequireTxSuccess(t, env.Submit(cash))
					env.Close()
					require.Equal(t, uint32(1), readAccountRoot(t, env, holder).SponsoredOwnerCount)
					require.Equal(t, uint32(1), readAccountRoot(t, env, sponsor).SponsoringOwnerCount)
				} else {
					cashIssuerCheck(t, env, holder, issuer, amount)
				}

				seq := env.Seq(holder)
				create := selfIOUEscrow(env, holder, amount).Build()
				if sponsoredObject == "Escrow" {
					sponsorReserve(create.GetCommon(), sponsor)
				}
				result := env.Submit(create)
				jtx.RequireTxSuccess(t, result)
				env.Close()

				requireExhaustedLineDeleted(t, env, result.Metadata, holder, issuer, side, false)
				holderRoot := readAccountRoot(t, env, holder)
				sponsorRoot := readAccountRoot(t, env, sponsor)
				require.Equal(t, uint32(1), holderRoot.OwnerCount)
				escrowSponsor, err := tx.LedgerEntrySponsorFromView(env.Ledger(), keylet.Escrow(holder.ID, seq), "Sponsor")
				require.NoError(t, err)
				if sponsoredObject == "Escrow" {
					require.Equal(t, uint32(1), holderRoot.SponsoredOwnerCount)
					require.Equal(t, uint32(1), sponsorRoot.SponsoringOwnerCount)
					require.Equal(t, sponsor.Address, escrowSponsor)
				} else {
					require.Zero(t, holderRoot.SponsoredOwnerCount)
					require.Zero(t, sponsorRoot.SponsoringOwnerCount)
					require.Empty(t, escrowSponsor)
				}
			})
		}
	}
}

// TestIOUEscrow_BatchCheckCashThenEscrowCreate replays the ledger 22280293
// shape: one Batch cashes an issuer check, auto-creating the holder's default
// line, then escrows the entire balance. The inner EscrowCreate must delete the
// line, release its reserve, and thread the issuer.
func TestIOUEscrow_BatchCheckCashThenEscrowCreate(t *testing.T) {
	for _, holderLow := range []bool{false, true} {
		t.Run(fmt.Sprintf("holderLow=%t", holderLow), func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			env.EnableFeature("TokenEscrow")
			env.EnableFeatureNow("BatchV1_1")
			env.Close()
			holder, issuer := newLineCleanupPair(t, env, holderLow, false)
			side := holderSide(holderLow)
			amount := tx.NewIssuedAmount(100_000_000, 0, "USD", issuer.Address)
			issuerOwners := env.OwnerCount(issuer)
			checkID := issuerCheck(t, env, holder, issuer, amount)

			seq := env.Seq(holder)
			create := selfIOUEscrow(env, holder, amount).Build()
			create.Fee = "0"
			create.SigningPubKey = ""
			create.SetSequence(seq + 2)
			create.SetFlags(tx.TfInnerBatchTxn)
			batch := batchtest.NewBatchBuilder(holder, seq, batchtest.CalcBatchFeeFromEnv(env, 0, 2), batchtx.BatchFlagAllOrNothing).
				AddInnerTx(batchtest.MakeInnerCheckCash(holder, checkID, amount, seq+1)).
				AddInnerTx(create).
				MustBuild()
			jtx.RequireTxSuccess(t, env.Submit(batch))
			env.Close()

			require.False(t, env.TrustLineExists(holder, issuer, "USD"))
			require.Equal(t, uint32(1), env.OwnerCount(holder))
			require.Equal(t, issuerOwners, env.OwnerCount(issuer))
			escrowKey := keylet.Escrow(holder.ID, seq+2)
			require.True(t, env.LedgerEntryExists(escrowKey))

			lineKey := keylet.Line(holder.ID, issuer.ID, "USD")
			nodes := closedInnerMetaNodes(t, env, batch.RawTransactions[1].RawTransaction.InnerTx)
			require.Len(t, nodes, 6)
			deleted, ok := nodes["DeletedNode/"+fmt.Sprintf("%X", lineKey.Key)]
			require.True(t, ok, "inner EscrowCreate must delete the exhausted line")
			require.Equal(t, side.noRipple, metadata.ToUint32(deleted["FinalFields"].(map[string]any)["Flags"]))
			_, ok = nodes["ModifiedNode/"+fmt.Sprintf("%X", keylet.Account(issuer.ID).Key)]
			require.True(t, ok, "inner EscrowCreate must thread the issuer AccountRoot")
		})
	}
}

// closedInnerMetaNodes returns the closed-ledger metadata nodes of a Batch
// inner transaction keyed by "<NodeType>/<LedgerIndex>".
func closedInnerMetaNodes(t *testing.T, env *jtx.TestEnv, inner tx.Transaction) map[string]map[string]any {
	t.Helper()
	id, err := tx.ComputeTransactionHash(inner)
	require.NoError(t, err)
	stored, found, err := env.LastClosedLedger().GetTransaction(id)
	require.NoError(t, err)
	require.True(t, found)
	_, metaBlob, err := tx.SplitTxWithMetaBlobStrict(stored)
	require.NoError(t, err)
	meta, err := binarycodec.DecodeBytes(metaBlob)
	require.NoError(t, err)
	require.Equal(t, "tesSUCCESS", meta["TransactionResult"])

	affected, ok := meta["AffectedNodes"].([]any)
	require.True(t, ok)
	nodes := make(map[string]map[string]any, len(affected))
	for _, wrapped := range affected {
		for nodeType, raw := range wrapped.(map[string]any) {
			node := raw.(map[string]any)
			nodes[nodeType+"/"+node["LedgerIndex"].(string)] = node
		}
	}
	return nodes
}

// issuerHoldsReceiverIOU makes the issuer hold amount of the receiver's own
// USD on their existing line, with the issuer's side otherwise default.
func issuerHoldsReceiverIOU(t *testing.T, env *jtx.TestEnv, issuer, receiver *jtx.Account, amount int64) {
	t.Helper()
	receiverUSD := func(v int64) tx.Amount {
		return tx.NewIssuedAmountFromFloat64(float64(v), "USD", receiver.Address)
	}
	jtx.RequireTxSuccess(t, env.Submit(trustset.TrustSet(issuer, receiverUSD(amount)).Build()))
	env.Close()
	jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(receiver, issuer, receiverUSD(amount)).Build()))
	env.Close()
	jtx.RequireTxSuccess(t, env.Submit(trustset.TrustSet(issuer, receiverUSD(0)).Build()))
	env.Close()
}

// TestIOUEscrow_UnlockReleasesIssuerHolding covers the unlock direction: when
// the issuer holds the receiver's IOU on a line it no longer trusts, crediting
// the escrow exhausts that holding and must release the issuer's reserve,
// whoever submits the finish or cancel.
func TestIOUEscrow_UnlockReleasesIssuerHolding(t *testing.T) {
	const amount = 50
	tests := []struct {
		name       string
		cancel     bool
		submitters []string
	}{
		{name: "Finish", submitters: []string{"issuer", "owner", "receiver"}},
		{name: "Cancel", cancel: true, submitters: []string{"issuer", "owner", "other"}},
	}

	for _, rules := range escrowReserveRuleCases() {
		for _, tt := range tests {
			for _, submitterRole := range tt.submitters {
				t.Run(fmt.Sprintf("%s/%s/submitter=%s", rules.name, tt.name, submitterRole), func(t *testing.T) {
					env := newEscrowReserveEnv(t, rules)
					gw := jtx.NewAccount("gateway")
					alice := jtx.NewAccount("alice")
					carol := jtx.NewAccount("carol")
					fund5000(env, gw, alice, carol)
					jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(gw).AllowTrustLineLocking().Build()))
					env.Close()
					env.Trust(alice, tx.NewIssuedAmountFromFloat64(10000, "USD", gw.Address))
					env.Trust(carol, tx.NewIssuedAmountFromFloat64(10000, "USD", gw.Address))
					env.Close()

					// carol receives the unlocked funds: as finish destination, or as
					// the refunded owner on cancel.
					owner, destination := alice, carol
					if tt.cancel {
						owner, destination = carol, alice
					}
					jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gw, owner, usd(amount, gw)).Build()))
					env.Close()
					seq := env.Seq(owner)
					create := escrow.EscrowCreate(owner, destination, 0).
						IOUAmount(usd(amount, gw)).
						FinishTime(env.Now().Add(time.Second))
					if tt.cancel {
						create.CancelTime(env.Now().Add(2 * time.Second))
					}
					jtx.RequireTxSuccess(t, env.Submit(create.Build()))
					env.Close()

					issuerHoldsReceiverIOU(t, env, gw, carol, amount)
					issuerReserve := holderSide(state.CompareAccountIDs(gw.ID, carol.ID) < 0).reserve
					require.NotZero(t, env.TrustLineFlags(gw, carol, "USD")&issuerReserve)
					gwOwners := env.OwnerCount(gw)
					env.SetTime(env.Now().Add(10 * time.Second))
					env.Close()

					submitters := map[string]*jtx.Account{
						"issuer": gw, "owner": owner, "receiver": carol, "other": alice,
					}
					submitter := submitters[submitterRole]
					var unlock tx.Transaction = escrow.EscrowFinish(submitter, owner, seq).Build()
					if tt.cancel {
						unlock = escrow.EscrowCancel(submitter, owner, seq).Build()
					}
					result := env.Submit(unlock)
					jtx.RequireTxSuccess(t, result)
					env.Close()

					require.False(t, env.LedgerEntryExists(keylet.Escrow(owner.ID, seq)))
					require.True(t, env.IOUBalance(carol, gw, "USD").IsZero())
					require.Zero(t, readTrustLine(t, env, gw, carol).Flags&issuerReserve)
					require.Equal(t, gwOwners-1, env.OwnerCount(gw))
					gwNode := metadata.FindNodeByAccount(result.Metadata, gw.Address)
					require.NotNil(t, gwNode)
					require.Contains(t, gwNode.PreviousFields, "OwnerCount")
				})
			}
		}
	}
}
