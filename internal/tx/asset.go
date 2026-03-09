package tx

// Asset represents an XRPL asset (currency + optional issuer)
type Asset struct {
	Currency string `json:"currency"`
	Issuer   string `json:"issuer,omitempty"`
}
