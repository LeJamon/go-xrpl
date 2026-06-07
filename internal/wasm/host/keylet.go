package host

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/internal/wasm"
	"github.com/LeJamon/go-xrpl/keylet"
)

func (e *Env) AccountKeylet(acct []byte) ([]byte, wasm.HostFunctionError) {
	a, herr := account(acct)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	return keyBytes(keylet.Account(a)), wasm.HfSuccess
}

func (e *Env) CheckKeylet(acct []byte, seq uint32) ([]byte, wasm.HostFunctionError) {
	a, herr := account(acct)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	return keyBytes(keylet.Check(a, seq)), wasm.HfSuccess
}

func (e *Env) DIDKeylet(acct []byte) ([]byte, wasm.HostFunctionError) {
	a, herr := account(acct)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	return keyBytes(keylet.DID(a)), wasm.HfSuccess
}

func (e *Env) EscrowKeylet(acct []byte, seq uint32) ([]byte, wasm.HostFunctionError) {
	a, herr := account(acct)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	return keyBytes(keylet.Escrow(a, seq)), wasm.HfSuccess
}

func (e *Env) NFTOfferKeylet(acct []byte, seq uint32) ([]byte, wasm.HostFunctionError) {
	a, herr := account(acct)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	return keyBytes(keylet.NFTokenOffer(a, seq)), wasm.HfSuccess
}

func (e *Env) OfferKeylet(acct []byte, seq uint32) ([]byte, wasm.HostFunctionError) {
	a, herr := account(acct)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	return keyBytes(keylet.Offer(a, seq)), wasm.HfSuccess
}

func (e *Env) OracleKeylet(acct []byte, documentID uint32) ([]byte, wasm.HostFunctionError) {
	a, herr := account(acct)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	return keyBytes(keylet.Oracle(a, documentID)), wasm.HfSuccess
}

func (e *Env) PermissionedDomainKeylet(acct []byte, seq uint32) ([]byte, wasm.HostFunctionError) {
	a, herr := account(acct)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	return keyBytes(keylet.PermissionedDomain(a, seq)), wasm.HfSuccess
}

func (e *Env) SignersKeylet(acct []byte) ([]byte, wasm.HostFunctionError) {
	a, herr := account(acct)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	return keyBytes(keylet.SignerList(a)), wasm.HfSuccess
}

func (e *Env) TicketKeylet(acct []byte, ticketSeq uint32) ([]byte, wasm.HostFunctionError) {
	a, herr := account(acct)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	return keyBytes(keylet.Ticket(a, ticketSeq)), wasm.HfSuccess
}

func (e *Env) VaultKeylet(acct []byte, seq uint32) ([]byte, wasm.HostFunctionError) {
	a, herr := account(acct)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	return keyBytes(keylet.Vault(a, seq)), wasm.HfSuccess
}

func (e *Env) DelegateKeylet(acct, authorize []byte) ([]byte, wasm.HostFunctionError) {
	a, herr := account(acct)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	auth, herr := account(authorize)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	if a == auth {
		return nil, wasm.HfInvalidParams
	}
	return keyBytes(keylet.DelegateKeylet(a, auth)), wasm.HfSuccess
}

func (e *Env) DepositPreauthKeylet(acct, authorize []byte) ([]byte, wasm.HostFunctionError) {
	a, herr := account(acct)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	auth, herr := account(authorize)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	if a == auth {
		return nil, wasm.HfInvalidParams
	}
	return keyBytes(keylet.DepositPreauth(a, auth)), wasm.HfSuccess
}

func (e *Env) PaychanKeylet(acct, destination []byte, seq uint32) ([]byte, wasm.HostFunctionError) {
	a, herr := account(acct)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	dst, herr := account(destination)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	if a == dst {
		return nil, wasm.HfInvalidParams
	}
	return keyBytes(keylet.PayChannel(a, dst, seq)), wasm.HfSuccess
}

func (e *Env) CredentialKeylet(subject, issuer, credentialType []byte) ([]byte, wasm.HostFunctionError) {
	subj, herr := account(subject)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	iss, herr := account(issuer)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	if len(credentialType) == 0 || len(credentialType) > maxCredentialTypeLength {
		return nil, wasm.HfInvalidParams
	}
	return keyBytes(keylet.Credential(subj, iss, credentialType)), wasm.HfSuccess
}

func (e *Env) MPTIssuanceKeylet(issuer []byte, seq uint32) ([]byte, wasm.HostFunctionError) {
	iss, herr := account(issuer)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	return keyBytes(keylet.MPTIssuanceBySeq(seq, iss)), wasm.HfSuccess
}

func (e *Env) MPTokenKeylet(mptid, holder []byte) ([]byte, wasm.HostFunctionError) {
	if len(mptid) != 24 {
		return nil, wasm.HfInvalidParams
	}
	var m [24]byte
	copy(m[:], mptid)
	if m == ([24]byte{}) {
		return nil, wasm.HfInvalidParams
	}
	h, herr := account(holder)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	return keyBytes(keylet.MPTokenByID(m, h)), wasm.HfSuccess
}

// LineKeylet derives a trust-line (RippleState) keylet. currency is the raw
// 20-byte currency code; keylet.Line takes the string form, and a 40-char hex
// string round-trips any 20 bytes exactly (currencyToBytes hex-decodes it).
func (e *Env) LineKeylet(account1, account2, currency []byte) ([]byte, wasm.HostFunctionError) {
	a1, herr := account(account1)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	a2, herr := account(account2)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	if a1 == a2 {
		return nil, wasm.HfInvalidParams
	}
	if len(currency) != 20 {
		return nil, wasm.HfInvalidParams
	}
	var cur [20]byte
	copy(cur[:], currency)
	if cur == ([20]byte{}) {
		return nil, wasm.HfInvalidParams
	}
	return keyBytes(keylet.Line(a1, a2, hex.EncodeToString(currency))), wasm.HfSuccess
}

// AMMKeylet derives an AMM keylet from two assets. Each asset is a 20-byte
// currency (XRP) or a 40-byte currency+issuer pair (IOU). The two assets must
// differ, mirroring rippled's ammKeylet (HostFuncImplKeylet.cpp).
func (e *Env) AMMKeylet(asset1, asset2 []byte) ([]byte, wasm.HostFunctionError) {
	cur1, iss1, herr := parseAsset(asset1)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	cur2, iss2, herr := parseAsset(asset2)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	if cur1 == cur2 && iss1 == iss2 {
		return nil, wasm.HfInvalidParams
	}
	return keyBytes(keylet.AMM(iss1, cur1, iss2, cur2)), wasm.HfSuccess
}

// parseAsset splits an asset wire encoding into its currency and issuer,
// enforcing rippled's getDataAsset (HostFuncWrapper.cpp): a bare 20-byte
// currency must be native (all-zero = XRP); a 40-byte currency+issuer must be
// non-native (not both zero). MPT assets (24 bytes) are not valid for AMM. An
// Issue is native iff it equals xrpIssue() (zero currency and zero issuer).
func parseAsset(b []byte) (currency, issuer [20]byte, herr wasm.HostFunctionError) {
	switch len(b) {
	case 20:
		copy(currency[:], b)
		if currency != ([20]byte{}) {
			return currency, issuer, wasm.HfInvalidParams
		}
	case 40:
		copy(currency[:], b[0:20])
		copy(issuer[:], b[20:40])
		if currency == ([20]byte{}) && issuer == ([20]byte{}) {
			return currency, issuer, wasm.HfInvalidParams
		}
	default:
		return currency, issuer, wasm.HfInvalidParams
	}
	return currency, issuer, wasm.HfSuccess
}
