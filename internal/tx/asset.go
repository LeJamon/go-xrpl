package tx

import (
	"encoding/json"
	"errors"

	"github.com/LeJamon/go-xrpl/keylet"
)

// Asset represents an XRPL asset: XRP, an issued currency (currency + issuer),
// or a multi-purpose token (MPTIssuanceID). Mirrors rippled's Asset = Issue |
// MPTIssue.
type Asset struct {
	Currency      string `json:"currency"`
	Issuer        string `json:"issuer,omitempty"`
	MPTIssuanceID string `json:"mpt_issuance_id,omitempty"`
}

// NormalizeNativeCurrency returns XRP for a textual representation of the zero currency.
func NormalizeNativeCurrency(currency string) string {
	if keylet.CurrencyBytes(currency) == [20]byte{} {
		return "XRP"
	}
	return currency
}

func (a *Asset) UnmarshalJSON(data []byte) error {
	type plain Asset
	value := plain(*a)
	fields := struct {
		*plain
		Currency *string `json:"currency"`
		Issuer   *string `json:"issuer"`
	}{plain: &value}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if fields.Issuer != nil {
		value.Issuer = *fields.Issuer
	}
	if fields.Currency != nil {
		value.Currency = *fields.Currency
		if value.MPTIssuanceID == "" {
			value.Currency = NormalizeNativeCurrency(value.Currency)
			if value.Currency == "XRP" && (fields.Issuer != nil || value.Issuer != "") {
				return errors.New("native asset must not specify issuer")
			}
		}
	}
	*a = Asset(value)
	return nil
}

// IsMPT reports whether the asset is a multi-purpose token.
func (a Asset) IsMPT() bool { return a.MPTIssuanceID != "" }

// IsNative reports whether the asset is native XRP.
func (a Asset) IsNative() bool {
	return !a.IsMPT() && a.Issuer == "" && NormalizeNativeCurrency(a.Currency) == "XRP"
}
