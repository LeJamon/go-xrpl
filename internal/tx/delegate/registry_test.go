package delegate_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	delegatetx "github.com/LeJamon/go-xrpl/internal/tx/delegate"
	"github.com/stretchr/testify/require"
)

func TestDelegateSet_RegistrationAndAmendment(t *testing.T) {
	all.RegisterAll()

	transaction, err := tx.NewFromType(tx.TypeDelegateSet)
	require.NoError(t, err)
	require.IsType(t, &delegatetx.DelegateSet{}, transaction)
	require.Equal(t, [][32]byte{amendment.FeaturePermissionDelegationV1_1}, transaction.RequiredAmendments())
}

func TestDelegateSet_EveryRegisteredTypeHasDelegationDecision(t *testing.T) {
	all.RegisterAll()
	expected := map[tx.Type]bool{
		tx.TypePayment:                      true,
		tx.TypeEscrowCreate:                 true,
		tx.TypeEscrowFinish:                 true,
		tx.TypeAccountSet:                   false,
		tx.TypeEscrowCancel:                 true,
		tx.TypeRegularKeySet:                false,
		tx.TypeOfferCreate:                  true,
		tx.TypeOfferCancel:                  true,
		tx.TypeTicketCreate:                 true,
		tx.TypeSignerListSet:                false,
		tx.TypePaymentChannelCreate:         true,
		tx.TypePaymentChannelFund:           true,
		tx.TypePaymentChannelClaim:          true,
		tx.TypeCheckCreate:                  true,
		tx.TypeCheckCash:                    true,
		tx.TypeCheckCancel:                  true,
		tx.TypeDepositPreauth:               true,
		tx.TypeTrustSet:                     true,
		tx.TypeAccountDelete:                false,
		tx.TypeNFTokenMint:                  true,
		tx.TypeNFTokenBurn:                  true,
		tx.TypeNFTokenCreateOffer:           true,
		tx.TypeNFTokenCancelOffer:           true,
		tx.TypeNFTokenAcceptOffer:           true,
		tx.TypeClawback:                     true,
		tx.TypeAMMClawback:                  true,
		tx.TypeAMMCreate:                    true,
		tx.TypeAMMDeposit:                   true,
		tx.TypeAMMWithdraw:                  true,
		tx.TypeAMMVote:                      true,
		tx.TypeAMMBid:                       true,
		tx.TypeAMMDelete:                    true,
		tx.TypeXChainCreateClaimID:          true,
		tx.TypeXChainCommit:                 true,
		tx.TypeXChainClaim:                  true,
		tx.TypeXChainAccountCreateCommit:    true,
		tx.TypeXChainAddClaimAttestation:    true,
		tx.TypeXChainAddAccountCreateAttest: true,
		tx.TypeXChainModifyBridge:           true,
		tx.TypeXChainCreateBridge:           true,
		tx.TypeDIDSet:                       true,
		tx.TypeDIDDelete:                    true,
		tx.TypeOracleSet:                    true,
		tx.TypeOracleDelete:                 true,
		tx.TypeLedgerStateFix:               true,
		tx.TypeMPTokenIssuanceCreate:        true,
		tx.TypeMPTokenIssuanceDestroy:       true,
		tx.TypeMPTokenIssuanceSet:           true,
		tx.TypeMPTokenAuthorize:             true,
		tx.TypeConfidentialMPTConvert:       false,
		tx.TypeConfidentialMPTMergeInbox:    true,
		tx.TypeConfidentialMPTConvertBack:   true,
		tx.TypeConfidentialMPTSend:          true,
		tx.TypeConfidentialMPTClawback:      true,
		tx.TypeCredentialCreate:             true,
		tx.TypeCredentialAccept:             true,
		tx.TypeCredentialDelete:             true,
		tx.TypeNFTokenModify:                true,
		tx.TypePermissionedDomainSet:        true,
		tx.TypePermissionedDomainDelete:     true,
		tx.TypeDelegateSet:                  false,
		tx.TypeVaultCreate:                  false,
		tx.TypeVaultSet:                     false,
		tx.TypeVaultDelete:                  false,
		tx.TypeVaultDeposit:                 false,
		tx.TypeVaultWithdraw:                false,
		tx.TypeVaultClawback:                false,
		tx.TypeBatch:                        false,
		tx.TypeLoanBrokerSet:                false,
		tx.TypeLoanBrokerDelete:             false,
		tx.TypeLoanBrokerCoverDeposit:       false,
		tx.TypeLoanBrokerCoverWithdraw:      false,
		tx.TypeLoanBrokerCoverClawback:      false,
		tx.TypeLoanSet:                      false,
		tx.TypeLoanDelete:                   false,
		tx.TypeLoanManage:                   false,
		tx.TypeLoanPay:                      false,
		tx.TypeSponsorshipTransfer:          false,
		tx.TypeSponsorshipSet:               true,
		tx.TypeAmendment:                    false,
		tx.TypeFee:                          false,
		tx.TypeUNLModify:                    false,
	}

	registered := tx.SupportedTypes()
	require.Len(t, registered, len(expected))
	features := amendment.AllFeatures()
	enabled := make([][32]byte, len(features))
	for i, feature := range features {
		enabled[i] = feature.ID
	}
	rules := amendment.NewRules(enabled)
	for _, transactionType := range registered {
		wantDelegatable, decided := expected[transactionType]
		require.Truef(t, decided, "transaction %s needs an explicit delegation decision", transactionType)

		ds := delegatetx.NewDelegateSet("")
		ds.Permissions = append(ds.Permissions, delegatetx.NewPermission(transactionType.String()))
		err := ds.PreflightRules(rules)
		if wantDelegatable {
			require.NoErrorf(t, err, "transaction %s", transactionType)
		} else {
			require.Errorf(t, err, "transaction %s", transactionType)
		}
	}
}
