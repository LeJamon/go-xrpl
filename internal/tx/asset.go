package tx

// Asset represents an XRPL asset: XRP, an issued currency (currency + issuer),
// or a multi-purpose token (MPTIssuanceID). Mirrors rippled's Asset = Issue |
// MPTIssue.
type Asset struct {
	Currency      string `json:"currency"`
	Issuer        string `json:"issuer,omitempty"`
	MPTIssuanceID string `json:"mpt_issuance_id,omitempty"`
}

// IsMPT reports whether the asset is a multi-purpose token.
func (a Asset) IsMPT() bool { return a.MPTIssuanceID != "" }

// IsNative reports whether the asset is native XRP.
func (a Asset) IsNative() bool {
	return !a.IsMPT() && a.Issuer == "" && (a.Currency == "" || a.Currency == "XRP")
}
