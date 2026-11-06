package invariants

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

const ownerDirLPCurrency = "03930D02208264E2E40EC1B0C09E4DB96EE197B1"

type ownerDirFixture struct {
	pseudo, other, issuer [20]byte
	pseudoAddr            string
	view                  mapView
}

func newOwnerDirFixture(t *testing.T) *ownerDirFixture {
	t.Helper()
	f := &ownerDirFixture{
		pseudo: [20]byte{0x70},
		other:  [20]byte{0x71},
		issuer: [20]byte{0x72},
		view:   mapView{data: map[[32]byte][]byte{}},
	}
	f.pseudoAddr = state.EncodeAccountIDSafe(f.pseudo)
	return f
}

func (f *ownerDirFixture) put(key [32]byte, data []byte) [32]byte {
	f.view.data[key] = data
	return key
}

func (f *ownerDirFixture) root(t *testing.T, root *state.AccountRoot) {
	t.Helper()
	root.Account = f.pseudoAddr
	root.Flags = state.LsfDisableMaster | state.LsfDefaultRipple | state.LsfDepositAuth
	f.put(keylet.Account(f.pseudo).Key, mustSerializeAccount(t, root))
}

func (f *ownerDirFixture) trustLine(t *testing.T, a, b [20]byte, currency string) [32]byte {
	t.Helper()
	amount := func(issuer [20]byte) map[string]any {
		return map[string]any{"currency": currency, "issuer": state.EncodeAccountIDSafe(issuer), "value": "0"}
	}
	key := keylet.Line(a, b, currency).Key
	return f.put(key, mustEncode(t, map[string]any{
		"LedgerEntryType":   "RippleState",
		"Flags":             uint32(0),
		"Balance":           map[string]any{"currency": currency, "issuer": "rrrrrrrrrrrrrrrrrrrrBZbvji", "value": "0"},
		"LowLimit":          amount(a),
		"HighLimit":         amount(b),
		"LowNode":           "0",
		"HighNode":          "0",
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	}))
}

func (f *ownerDirFixture) mptoken(t *testing.T, id [24]byte, holder [20]byte) [32]byte {
	t.Helper()
	data, err := state.SerializeMPToken(&state.MPTokenData{Account: holder, MPTokenIssuanceID: id})
	if err != nil {
		t.Fatalf("serialize MPToken: %v", err)
	}
	return f.put(keylet.MPTokenByID(id, holder).Key, data)
}

func (f *ownerDirFixture) credential(t *testing.T) [32]byte {
	t.Helper()
	key := keylet.Credential(f.pseudo, f.other, []byte("KYC")).Key
	return f.put(key, mustEncode(t, map[string]any{
		"LedgerEntryType":   "Credential",
		"Flags":             uint32(0),
		"Subject":           f.pseudoAddr,
		"Issuer":            state.EncodeAccountIDSafe(f.other),
		"CredentialType":    "4B5943",
		"SubjectNode":       "0",
		"IssuerNode":        "0",
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	}))
}

func (f *ownerDirFixture) object(t *testing.T, key [32]byte, fields map[string]any) [32]byte {
	t.Helper()
	fields["Flags"] = uint32(0)
	fields["PreviousTxnID"] = strings.Repeat("0", 64)
	fields["PreviousTxnLgrSeq"] = uint32(0)
	return f.put(key, mustEncode(t, fields))
}

func (f *ownerDirFixture) vault(t *testing.T, vaultID [32]byte, asset [24]byte, share [24]byte) {
	t.Helper()
	f.object(t, keylet.VaultByID(vaultID).Key, map[string]any{
		"LedgerEntryType":  "Vault",
		"Account":          f.pseudoAddr,
		"Owner":            state.EncodeAccountIDSafe(f.other),
		"OwnerNode":        "0",
		"Sequence":         uint32(1),
		"Asset":            map[string]any{"mpt_issuance_id": hexUpper(asset[:])},
		"ShareMPTID":       hexUpper(share[:]),
		"WithdrawalPolicy": uint8(1),
	})
}

func (f *ownerDirFixture) loanBroker(t *testing.T, brokerID, vaultID [32]byte, account string) [32]byte {
	t.Helper()
	return f.object(t, keylet.LoanBrokerByID(brokerID).Key, map[string]any{
		"LedgerEntryType": "LoanBroker",
		"Account":         account,
		"Owner":           state.EncodeAccountIDSafe(f.other),
		"OwnerNode":       "0",
		"VaultNode":       "0",
		"VaultID":         hexUpper(vaultID[:]),
		"Sequence":        uint32(1),
		"LoanSequence":    uint32(1),
	})
}

// added links index into the pseudo-account's owner directory.
func (f *ownerDirFixture) added(t *testing.T, owner [20]byte, indexes ...[32]byte) []InvariantEntry {
	t.Helper()
	data, err := state.SerializeDirectoryNode(&state.DirectoryNode{
		Owner:     owner,
		RootIndex: keylet.OwnerDir(owner).Key,
		Indexes:   indexes,
	}, false)
	if err != nil {
		t.Fatalf("serialize DirectoryNode: %v", err)
	}
	return []InvariantEntry{{Key: keylet.OwnerDir(owner).Key, EntryType: entry.TypeDirectoryNode, After: data}}
}

// relinked appends index to a page that already held existing.
func (f *ownerDirFixture) relinked(t *testing.T, owner [20]byte, existing, index [32]byte) []InvariantEntry {
	t.Helper()
	page := func(indexes ...[32]byte) []byte {
		data, err := state.SerializeDirectoryNode(&state.DirectoryNode{Owner: owner, RootIndex: keylet.OwnerDir(owner).Key, Indexes: indexes}, false)
		if err != nil {
			t.Fatalf("serialize DirectoryNode: %v", err)
		}
		return data
	}
	return []InvariantEntry{{Key: keylet.OwnerDir(owner).Key, EntryType: entry.TypeDirectoryNode, Before: page(existing), After: page(existing, index)}}
}

func hexUpper(b []byte) string {
	return strings.ToUpper(hex.EncodeToString(b))
}

func TestPseudoAccountOwnerDirectory(t *testing.T) {
	fixed := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_5_0})
	legacy := amendment.NewRules(nil)

	type check struct {
		name    string
		build   func(t *testing.T, f *ownerDirFixture) []InvariantEntry
		wantErr string
	}

	amm := func(t *testing.T, f *ownerDirFixture) [32]byte {
		ammID := [32]byte{0xA1}
		f.root(t, &state.AccountRoot{AMMID: ammID})
		f.object(t, keylet.AMMByID(ammID).Key, map[string]any{
			"LedgerEntryType": "AMM",
			"Account":         f.pseudoAddr,
			"Asset":           map[string]any{"currency": "XRP"},
			"Asset2":          map[string]any{"currency": "USD", "issuer": state.EncodeAccountIDSafe(f.issuer)},
			"LPTokenBalance":  map[string]any{"currency": ownerDirLPCurrency, "issuer": f.pseudoAddr, "value": "100"},
			"OwnerNode":       "0",
		})
		return ammID
	}
	assetID := keylet.MakeMPTID(3, [20]byte{0x72})
	shareID := keylet.MakeMPTID(4, [20]byte{0x70})
	vaultID := [32]byte{0xB1}
	vault := func(t *testing.T, f *ownerDirFixture) {
		f.root(t, &state.AccountRoot{VaultID: vaultID})
		f.vault(t, vaultID, assetID, shareID)
	}
	brokerID := [32]byte{0xC1}
	broker := func(t *testing.T, f *ownerDirFixture) {
		f.root(t, &state.AccountRoot{LoanBrokerID: brokerID})
		f.loanBroker(t, brokerID, vaultID, f.pseudoAddr)
		f.object(t, keylet.VaultByID(vaultID).Key, map[string]any{
			"LedgerEntryType":  "Vault",
			"Account":          state.EncodeAccountIDSafe([20]byte{0x7F}),
			"Owner":            state.EncodeAccountIDSafe(f.other),
			"OwnerNode":        "0",
			"Sequence":         uint32(1),
			"Asset":            map[string]any{"mpt_issuance_id": hexUpper(assetID[:])},
			"ShareMPTID":       hexUpper(shareID[:]),
			"WithdrawalPolicy": uint8(1),
		})
	}
	loan := func(t *testing.T, f *ownerDirFixture, broker [32]byte) [32]byte {
		return f.object(t, keylet.LoanByID([32]byte{0xD1}).Key, map[string]any{
			"LedgerEntryType": "Loan",
			"LoanBrokerID":    hexUpper(broker[:]),
			"Borrower":        state.EncodeAccountIDSafe(f.other),
			"OwnerNode":       "0",
			"LoanBrokerNode":  "0",
			"LoanSequence":    uint32(1),
			"StartDate":       uint32(1),
			"PaymentInterval": uint32(60),
			"PeriodicPayment": "1",
		})
	}

	checks := []check{
		{name: "AMM entry", build: func(t *testing.T, f *ownerDirFixture) []InvariantEntry {
			return f.added(t, f.pseudo, amm(t, f))
		}},
		{name: "AMM pool trust line", build: func(t *testing.T, f *ownerDirFixture) []InvariantEntry {
			amm(t, f)
			return f.added(t, f.pseudo, f.trustLine(t, f.pseudo, f.issuer, "USD"))
		}},
		{name: "AMM LP token line", build: func(t *testing.T, f *ownerDirFixture) []InvariantEntry {
			amm(t, f)
			return f.added(t, f.pseudo, f.trustLine(t, f.pseudo, f.other, ownerDirLPCurrency))
		}},
		{name: "AMM pinned by credential", wantErr: "may not own", build: func(t *testing.T, f *ownerDirFixture) []InvariantEntry {
			amm(t, f)
			return f.added(t, f.pseudo, f.credential(t))
		}},
		{name: "AMM unrelated trust line", wantErr: "may not own", build: func(t *testing.T, f *ownerDirFixture) []InvariantEntry {
			amm(t, f)
			return f.added(t, f.pseudo, f.trustLine(t, f.pseudo, f.other, "EUR"))
		}},
		{name: "vault share issuance", build: func(t *testing.T, f *ownerDirFixture) []InvariantEntry {
			vault(t, f)
			share := f.put(keylet.MPTIssuance(shareID).Key, mustSerializeMPTIssuance(t, &state.MPTokenIssuanceData{Issuer: f.pseudo, Sequence: 4}))
			return f.added(t, f.pseudo, share)
		}},
		{name: "vault asset holding", build: func(t *testing.T, f *ownerDirFixture) []InvariantEntry {
			vault(t, f)
			return f.added(t, f.pseudo, f.mptoken(t, assetID, f.pseudo))
		}},
		{name: "vault own loan broker", build: func(t *testing.T, f *ownerDirFixture) []InvariantEntry {
			vault(t, f)
			return f.added(t, f.pseudo, f.loanBroker(t, [32]byte{0xC2}, vaultID, state.EncodeAccountIDSafe([20]byte{0x7E})))
		}},
		{name: "vault other vault's loan broker", wantErr: "may not own", build: func(t *testing.T, f *ownerDirFixture) []InvariantEntry {
			vault(t, f)
			return f.added(t, f.pseudo, f.loanBroker(t, [32]byte{0xC3}, [32]byte{0xB2}, state.EncodeAccountIDSafe([20]byte{0x7E})))
		}},
		{name: "vault pinned by credential", wantErr: "may not own", build: func(t *testing.T, f *ownerDirFixture) []InvariantEntry {
			vault(t, f)
			return f.added(t, f.pseudo, f.credential(t))
		}},
		{name: "vault missing object", wantErr: "missing object", build: func(t *testing.T, f *ownerDirFixture) []InvariantEntry {
			vault(t, f)
			return f.added(t, f.pseudo, [32]byte{0xEE})
		}},
		{name: "broker cover holding", build: func(t *testing.T, f *ownerDirFixture) []InvariantEntry {
			broker(t, f)
			return f.added(t, f.pseudo, f.mptoken(t, assetID, f.pseudo))
		}},
		{name: "broker own loan", build: func(t *testing.T, f *ownerDirFixture) []InvariantEntry {
			broker(t, f)
			return f.added(t, f.pseudo, loan(t, f, brokerID))
		}},
		{name: "broker other broker's loan", wantErr: "may not own", build: func(t *testing.T, f *ownerDirFixture) []InvariantEntry {
			broker(t, f)
			return f.added(t, f.pseudo, loan(t, f, [32]byte{0xC9}))
		}},
		{name: "broker pinned by credential", wantErr: "may not own", build: func(t *testing.T, f *ownerDirFixture) []InvariantEntry {
			broker(t, f)
			return f.added(t, f.pseudo, f.credential(t))
		}},
		{name: "AMM foreign AMM entry", wantErr: "may not own", build: func(t *testing.T, f *ownerDirFixture) []InvariantEntry {
			amm(t, f)
			foreign := state.EncodeAccountIDSafe([20]byte{0x7D})
			return f.added(t, f.pseudo, f.object(t, keylet.AMMByID([32]byte{0xA2}).Key, map[string]any{
				"LedgerEntryType": "AMM",
				"Account":         foreign,
				"Asset":           map[string]any{"currency": "XRP"},
				"Asset2":          map[string]any{"currency": "EUR", "issuer": state.EncodeAccountIDSafe(f.issuer)},
				"LPTokenBalance":  map[string]any{"currency": ownerDirLPCurrency, "issuer": foreign, "value": "100"},
				"OwnerNode":       "0",
			}))
		}},
		{name: "vault foreign share issuance", wantErr: "may not own", build: func(t *testing.T, f *ownerDirFixture) []InvariantEntry {
			vault(t, f)
			foreign := keylet.MakeMPTID(9, f.pseudo)
			return f.added(t, f.pseudo, f.put(keylet.MPTIssuance(foreign).Key, mustSerializeMPTIssuance(t, &state.MPTokenIssuanceData{Issuer: f.pseudo, Sequence: 9})))
		}},
		{name: "vault foreign MPToken", wantErr: "may not own", build: func(t *testing.T, f *ownerDirFixture) []InvariantEntry {
			vault(t, f)
			return f.added(t, f.pseudo, f.mptoken(t, keylet.MakeMPTID(5, f.issuer), f.pseudo))
		}},
		{name: "broker foreign MPToken", wantErr: "may not own", build: func(t *testing.T, f *ownerDirFixture) []InvariantEntry {
			broker(t, f)
			return f.added(t, f.pseudo, f.mptoken(t, keylet.MakeMPTID(5, f.issuer), f.pseudo))
		}},
		{name: "pre-existing link is not rechecked", build: func(t *testing.T, f *ownerDirFixture) []InvariantEntry {
			ammKey := amm(t, f)
			return f.relinked(t, f.pseudo, f.credential(t), ammKey)
		}},
		{name: "regular account owner", build: func(t *testing.T, f *ownerDirFixture) []InvariantEntry {
			f.put(keylet.Account(f.other).Key, mustSerializeAccount(t, &state.AccountRoot{Account: state.EncodeAccountIDSafe(f.other), Sequence: 1}))
			return f.added(t, f.other, f.credential(t))
		}},
	}

	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			f := newOwnerDirFixture(t)
			entries := c.build(t, f)
			v := checkPseudoAccountOwnerDirs(entries, f.view, fixed)
			switch {
			case c.wantErr == "" && v != nil:
				t.Fatalf("unexpected violation %v", v)
			case c.wantErr != "" && (v == nil || !strings.Contains(v.Message, c.wantErr)):
				t.Fatalf("violation = %v, want %q", v, c.wantErr)
			}
			if v := checkPseudoAccountOwnerDirs(entries, f.view, legacy); v != nil {
				t.Fatalf("without fixCleanup3_5_0: unexpected violation %v", v)
			}
		})
	}
}

// readErrorView fails every read of one AccountRoot.
type readErrorView struct {
	mapView
	account [20]byte
}

func (v readErrorView) Read(k keylet.Keylet) ([]byte, error) {
	if k.Key == keylet.Account(v.account).Key {
		return nil, errors.New("read failed")
	}
	return v.mapView.Read(k)
}

func TestPseudoAccountOwnerDirectoryReadError(t *testing.T) {
	f := newOwnerDirFixture(t)
	view := readErrorView{mapView: f.view, account: f.pseudo}
	v := checkPseudoAccountOwnerDirs(f.added(t, f.pseudo, f.credential(t)), view, amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_5_0}))
	if v == nil || !strings.Contains(v.Message, "could not read owner AccountRoot") {
		t.Fatalf("violation = %v, want an owner read failure", v)
	}
}
