package keylet

import (
	"testing"

	"github.com/LeJamon/go-xrpl/ledger/entry/schema"
)

// TestEveryLedgerEntryTypeHasKeylet fails when a ledger-entry type in the
// schema has no keylet constructor, or one that reports a different type.
func TestEveryLedgerEntryTypeHasKeylet(t *testing.T) {
	var account [20]byte
	var key [32]byte
	keylets := map[string]Keylet{
		"AccountRoot":                     Account(account),
		"Amendments":                      Amendments(),
		"AMM":                             AMMByID(key),
		"Bridge":                          Bridge(account, account),
		"Check":                           Check(account, 1),
		"Credential":                      CredentialByID(key),
		"Delegate":                        Delegate(account, account),
		"DepositPreauth":                  DepositPreauth(account, account),
		"DID":                             DID(account),
		"DirectoryNode":                   OwnerDir(account),
		"Escrow":                          Escrow(account, 1),
		"FeeSettings":                     Fees(),
		"LedgerHashes":                    LedgerHashes(),
		"Loan":                            LoanByID(key),
		"LoanBroker":                      LoanBrokerByID(key),
		"MPToken":                         MPToken(key, account),
		"MPTokenIssuance":                 MPTIssuance([24]byte{}),
		"NegativeUNL":                     NegativeUNL(),
		"NFTokenOffer":                    NFTokenOffer(account, 1),
		"NFTokenPage":                     NFTokenPageMin(account),
		"Offer":                           Offer(account, 1),
		"Oracle":                          Oracle(account, 1),
		"PayChannel":                      PayChannel(account, account, 1),
		"PermissionedDomain":              PermissionedDomainByID(key),
		"RippleState":                     Line(account, account, "USD"),
		"SignerList":                      SignerList(account),
		"Sponsorship":                     Sponsorship(account, account),
		"Ticket":                          Ticket(account, 1),
		"Vault":                           VaultByID(key),
		"XChainOwnedClaimID":              XChainClaimID(XChainBridge{}, 1),
		"XChainOwnedCreateAccountClaimID": XChainCreateAccountClaimID(XChainBridge{}, 1),
	}

	known := make(map[string]bool, len(schema.Specs))
	for _, spec := range schema.Specs {
		known[spec.Name] = true
		k, ok := keylets[spec.Name]
		if !ok {
			t.Errorf("ledger entry type %s has no keylet constructor in this table", spec.Name)
			continue
		}
		if got := k.Type.String(); got != spec.Name {
			t.Errorf("keylet for %s reports type %s", spec.Name, got)
		}
	}
	for name := range keylets {
		if !known[name] {
			t.Errorf("keylet table lists %s, which is not in schema.Specs", name)
		}
	}
}
