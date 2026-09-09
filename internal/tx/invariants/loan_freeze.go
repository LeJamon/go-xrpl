package invariants

import (
	"encoding/hex"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
)

const tfLoanDefault uint32 = 0x00010000

// loanDefaultFreezeExemption identifies the only two pseudo-account transfers
// that a defaulted loan may make while the cleanup fix is enabled. The asset is
// kept with the account pair so a default on one vault cannot exempt another
// currency or issuance.
type loanDefaultFreezeExemption struct {
	issuer string
	broker string
	vault  string
	asset  Asset
}

func loanDefaultFreezeExemptionFor(tx Transaction, view ReadView, rules *amendment.Rules) *loanDefaultFreezeExemption {
	if rules == nil || !rules.Enabled(amendment.FeatureFixCleanup3_4_0) ||
		tx.TxType() != protocol.TxTypeLoanManage || view == nil {
		return nil
	}
	fields, err := tx.Flatten()
	if err != nil || u32Field(fields, "Flags")&tfLoanDefault == 0 {
		return nil
	}
	loanID, ok := hash256Field(fields, "LoanID")
	if !ok {
		return nil
	}
	loanFields, ok := readDecodedEntry(view, keylet.LoanByID(loanID))
	if !ok {
		return nil
	}
	brokerID, ok := hash256Field(loanFields, "LoanBrokerID")
	if !ok {
		return nil
	}
	brokerFields, ok := readDecodedEntry(view, keylet.LoanBrokerByID(brokerID))
	if !ok {
		return nil
	}
	vaultID, ok := hash256Field(brokerFields, "VaultID")
	if !ok {
		return nil
	}
	broker, ok := stringField(brokerFields, "Account")
	if !ok || broker == "" {
		return nil
	}
	vaultFields, ok := readDecodedEntry(view, keylet.VaultByID(vaultID))
	if !ok {
		return nil
	}
	vault, ok := stringField(vaultFields, "Account")
	if !ok || vault == "" {
		return nil
	}
	assetMap, ok := vaultFields["Asset"].(map[string]any)
	if !ok {
		return nil
	}
	asset := assetFromDecodedMap(assetMap)
	if asset.IsNative() {
		return nil
	}
	if asset.IsMPT() {
		// MPT lock handling is checked by ValidMPTTransfer. Keeping this helper
		// available for that invariant ensures both paths use the same chain.
		issuer := assetIssuerForMPT(asset.MPTIssuanceID)
		if issuer == "" {
			return nil
		}
		return &loanDefaultFreezeExemption{issuer: issuer, broker: broker, vault: vault, asset: asset}
	}
	if asset.Issuer == "" || asset.Currency == "" {
		return nil
	}
	return &loanDefaultFreezeExemption{issuer: asset.Issuer, broker: broker, vault: vault, asset: asset}
}

func readDecodedEntry(view ReadView, key keylet.Keylet) (map[string]any, bool) {
	data, err := view.Read(key)
	if err != nil || len(data) == 0 {
		return nil, false
	}
	decoded, err := decodeEntry(data)
	if err != nil {
		return nil, false
	}
	return decoded, true
}

func hash256Field(fields map[string]any, name string) ([32]byte, bool) {
	var result [32]byte
	s, ok := fields[name].(string)
	if !ok {
		return result, false
	}
	result, err := hexDecode32(s)
	return result, err == nil
}

func stringField(fields map[string]any, name string) (string, bool) {
	s, ok := fields[name].(string)
	return s, ok
}

func assetFromDecodedMap(fields map[string]any) Asset {
	if id, ok := fields["mpt_issuance_id"].(string); ok && id != "" {
		return Asset{MPTIssuanceID: strings.ToUpper(id)}
	}
	currency, _ := fields["currency"].(string)
	issuer, _ := fields["issuer"].(string)
	return Asset{Currency: currency, Issuer: issuer}
}

func assetIssuerForMPT(id string) string {
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != 24 {
		return ""
	}
	var issuer [20]byte
	copy(issuer[:], decoded[4:])
	encoded, err := state.EncodeAccountID(issuer)
	if err != nil {
		return ""
	}
	return encoded
}
