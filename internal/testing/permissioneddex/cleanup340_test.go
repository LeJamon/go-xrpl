package permissioneddex

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	cred "github.com/LeJamon/go-xrpl/internal/testing/credential"
	offer "github.com/LeJamon/go-xrpl/internal/testing/offer"
	payment "github.com/LeJamon/go-xrpl/internal/testing/payment"
	pd "github.com/LeJamon/go-xrpl/internal/testing/permissioneddomain"
	"github.com/LeJamon/go-xrpl/keylet"

	"github.com/stretchr/testify/require"
)

func TestPermissionedDEXCleanup340ExpiredCredentials(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, kind := range []string{"offer", "payment", "both"} {
			t.Run(fmt.Sprintf("cleanup=%t/%s", enabled, kind), func(t *testing.T) {
				env := jtx.NewTestEnv(t)
				if enabled {
					env.EnableFeature("fixCleanup3_4_0")
				}
				dex := SetupPermissionedDEX(t, env)
				members := []*jtx.Account{dex.Alice}
				if kind == "both" {
					members = append(members, dex.Bob)
				}
				expiry := env.NowRipple() + 100
				for _, member := range members {
					jtx.RequireTxSuccess(t, env.Submit(cred.CredentialDeleteHex(dex.DomainOwner, member, dex.DomainOwner, dex.CredType).Build()))
					jtx.RequireTxSuccess(t, env.Submit(cred.CredentialCreateHex(dex.DomainOwner, member, dex.CredType).Expiration(expiry).Build()))
					jtx.RequireTxSuccess(t, env.Submit(cred.CredentialAcceptHex(member, dex.DomainOwner, dex.CredType).Build()))
				}
				env.AdvanceTime(120 * time.Second)
				env.Close()
				balance, seq := env.Balance(dex.Alice), env.Seq(dex.Alice)
				counts := make([]uint32, len(members))
				for i, member := range members {
					counts[i] = env.OwnerCount(member)
				}
				var result jtx.TxResult
				if kind == "offer" {
					result = env.Submit(offer.OfferCreate(dex.Alice, jtx.XRPTxAmount(10_000_000), dex.USD(10)).DomainID(dex.DomainID).Build())
				} else {
					result = env.Submit(payment.PayIssued(dex.Alice, dex.Bob, dex.USD(10)).DomainID(dex.DomainIDHex).Build())
				}
				expected := "tecNO_PERMISSION"
				if enabled {
					expected = "tecEXPIRED"
				}
				jtx.RequireTxClaimed(t, result, expected)
				require.Equal(t, seq+1, env.Seq(dex.Alice))
				require.Equal(t, balance-10, env.Balance(dex.Alice))
				require.Equal(t, uint64(10), result.Fee)
				require.NotNil(t, result.Metadata)
				deleted := make(map[string]bool)
				for _, node := range result.Metadata.AffectedNodes {
					if node.LedgerEntryType == "Credential" {
						require.Equal(t, "DeletedNode", node.NodeType)
						deleted[strings.ToUpper(node.LedgerIndex)] = true
					}
				}
				if enabled {
					require.Len(t, deleted, len(members))
				} else {
					require.Empty(t, deleted)
				}
				ct, err := hex.DecodeString(dex.CredType)
				require.NoError(t, err)
				for i, member := range members {
					raw, err := env.Ledger().Read(keylet.Credential(member.ID, dex.DomainOwner.ID, ct))
					require.NoError(t, err)
					if enabled {
						credentialKey := keylet.Credential(member.ID, dex.DomainOwner.ID, ct).Key
						require.True(t, deleted[fmt.Sprintf("%X", credentialKey)])
						require.Nil(t, raw)
						require.Equal(t, counts[i]-1, env.OwnerCount(member))
					} else {
						require.NotNil(t, raw)
						require.Equal(t, counts[i], env.OwnerCount(member))
					}
				}
				jtx.RequireIOUBalance(t, env, dex.Alice, dex.GW, "USD", 100)
				jtx.RequireIOUBalance(t, env, dex.Bob, dex.GW, "USD", 100)
				offer.RequireOfferCount(t, env, dex.Alice, 0)
			})
		}
	}
}

func TestPermissionedDEXCleanup340ReplaceDomain(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("cleanup=%t", enabled), func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			if enabled {
				env.EnableFeature("fixCleanup3_4_0")
			}
			dex := SetupPermissionedDEX(t, env)
			seq := env.Seq(dex.DomainOwner)
			jtx.RequireTxSuccess(t, env.Submit(pd.DomainSet(dex.DomainOwner).Credential(dex.DomainOwner, dex.CredType).Build()))
			newDomain := keylet.PermissionedDomain(dex.DomainOwner.ID, seq).Key
			oldSeq := env.Seq(dex.Alice)
			jtx.RequireTxSuccess(t, env.Submit(offer.OfferCreate(dex.Alice, jtx.XRPTxAmount(10_000_000), dex.USD(10)).DomainID(dex.DomainID).Build()))
			newSeq := env.Seq(dex.Alice)
			result := env.Submit(offer.OfferCreate(dex.Alice, jtx.XRPTxAmount(20_000_000), dex.USD(20)).DomainID(newDomain).OfferSequence(oldSeq).Build())
			if enabled {
				jtx.RequireTxSuccess(t, result)
				offer.RequireNoOfferInLedger(t, env, dex.Alice, oldSeq)
				offer.RequireOfferInLedger(t, env, dex.Alice, newSeq)
			} else {
				jtx.RequireTxClaimed(t, result, "tecINVARIANT_FAILED")
				offer.RequireOfferInLedger(t, env, dex.Alice, oldSeq)
				offer.RequireNoOfferInLedger(t, env, dex.Alice, newSeq)
			}
		})
	}
}
