package schema

import "sort"

// InnerFieldStyle describes the serialization presence rule for a field in an
// embedded STObject.
type InnerFieldStyle uint8

const (
	// InnerRequired marks a field that must be present in every nested object.
	InnerRequired InnerFieldStyle = iota
	// InnerOptional marks a field whose absence has no implicit value.
	InnerOptional
	// InnerDefault marks a field that may be omitted when it has its type default.
	InnerDefault
)

// InnerValueKind describes the JSON representation accepted by an embedded
// STObject field. The ledger-entry decoder and the generated typed wrappers
// share this schema.
type InnerValueKind uint8

const (
	// InnerAny accepts the field's native JSON representation.
	InnerAny InnerValueKind = iota
	// InnerString accepts a JSON string.
	InnerString
	// InnerUInt8 accepts an unsigned 8-bit integer representation.
	InnerUInt8
	// InnerUInt16 accepts an unsigned 16-bit integer representation.
	InnerUInt16
	// InnerUInt32 accepts an unsigned 32-bit integer representation.
	InnerUInt32
	// InnerUInt64 accepts an unsigned 64-bit integer representation.
	InnerUInt64
	// InnerPermissionValue accepts a permission bitmask representation.
	InnerPermissionValue
	// InnerArray accepts a nested array representation.
	InnerArray
)

// InnerFieldTemplate describes one nested object's presence rule and value kind.
type InnerFieldTemplate struct {
	// Style controls whether omission is required, optional, or defaultable.
	Style InnerFieldStyle
	// Kind identifies the JSON representation accepted by the field.
	Kind InnerValueKind
}

// InnerObjectTemplate describes the fields accepted by one nested object.
type InnerObjectTemplate struct {
	// Fields maps XRPL field names to their nested serialization templates.
	Fields map[string]InnerFieldTemplate
}

var arrayElementTemplates = map[string]string{
	"AcceptedCredentials":             "Credential",
	"AdditionalBooks":                 "Book",
	"AuthAccounts":                    "AuthAccount",
	"AuthorizeCredentials":            "Credential",
	"DisabledValidators":              "DisabledValidator",
	"Majorities":                      "Majority",
	"NFTokens":                        "NFToken",
	"Permissions":                     "Permission",
	"PriceDataSeries":                 "PriceData",
	"SignerEntries":                   "SignerEntry",
	"VoteSlots":                       "VoteEntry",
	"XChainClaimAttestations":         "XChainClaimProofSig",
	"XChainCreateAccountAttestations": "XChainCreateAccountProofSig",
}

var innerObjectTemplates = map[string]InnerObjectTemplate{
	"SignerEntry": {
		Fields: map[string]InnerFieldTemplate{
			"Account":       {Style: InnerRequired, Kind: InnerString},
			"SignerWeight":  {Style: InnerRequired, Kind: InnerUInt16},
			"WalletLocator": {Style: InnerOptional, Kind: InnerString},
		},
	},
	"Majority": {
		Fields: map[string]InnerFieldTemplate{
			"Amendment": {Style: InnerRequired, Kind: InnerString},
			"CloseTime": {Style: InnerRequired, Kind: InnerUInt32},
		},
	},
	"DisabledValidator": {
		Fields: map[string]InnerFieldTemplate{
			"PublicKey":           {Style: InnerRequired, Kind: InnerString},
			"FirstLedgerSequence": {Style: InnerRequired, Kind: InnerUInt32},
		},
	},
	"NFToken": {
		Fields: map[string]InnerFieldTemplate{
			"NFTokenID": {Style: InnerRequired, Kind: InnerString},
			"URI":       {Style: InnerOptional, Kind: InnerString},
		},
	},
	"VoteEntry": {
		Fields: map[string]InnerFieldTemplate{
			"Account":    {Style: InnerRequired, Kind: InnerString},
			"TradingFee": {Style: InnerDefault, Kind: InnerUInt16},
			"VoteWeight": {Style: InnerRequired, Kind: InnerUInt32},
		},
	},
	"AuctionSlot": {
		Fields: map[string]InnerFieldTemplate{
			"Account":       {Style: InnerRequired, Kind: InnerString},
			"Expiration":    {Style: InnerRequired, Kind: InnerUInt32},
			"DiscountedFee": {Style: InnerDefault, Kind: InnerUInt16},
			"Price":         {Style: InnerRequired},
			"AuthAccounts":  {Style: InnerOptional, Kind: InnerArray},
		},
	},
	"XChainClaimAttestationCollectionElement": {
		Fields: map[string]InnerFieldTemplate{
			"AttestationSignerAccount": {Style: InnerRequired, Kind: InnerString},
			"PublicKey":                {Style: InnerRequired, Kind: InnerString},
			"Signature":                {Style: InnerRequired, Kind: InnerString},
			"Amount":                   {Style: InnerRequired},
			"Account":                  {Style: InnerRequired, Kind: InnerString},
			"AttestationRewardAccount": {Style: InnerRequired, Kind: InnerString},
			"WasLockingChainSend":      {Style: InnerRequired, Kind: InnerUInt8},
			"XChainClaimID":            {Style: InnerRequired, Kind: InnerUInt64},
			"Destination":              {Style: InnerOptional, Kind: InnerString},
		},
	},
	"XChainCreateAccountAttestationCollectionElement": {
		Fields: map[string]InnerFieldTemplate{
			"AttestationSignerAccount": {Style: InnerRequired, Kind: InnerString},
			"PublicKey":                {Style: InnerRequired, Kind: InnerString},
			"Signature":                {Style: InnerRequired, Kind: InnerString},
			"Amount":                   {Style: InnerRequired},
			"Account":                  {Style: InnerRequired, Kind: InnerString},
			"AttestationRewardAccount": {Style: InnerRequired, Kind: InnerString},
			"WasLockingChainSend":      {Style: InnerRequired, Kind: InnerUInt8},
			"XChainAccountCreateCount": {Style: InnerRequired, Kind: InnerUInt64},
			"Destination":              {Style: InnerRequired, Kind: InnerString},
			"SignatureReward":          {Style: InnerRequired},
		},
	},
	"XChainClaimProofSig": {
		Fields: map[string]InnerFieldTemplate{
			"AttestationSignerAccount": {Style: InnerRequired, Kind: InnerString},
			"PublicKey":                {Style: InnerRequired, Kind: InnerString},
			"Amount":                   {Style: InnerRequired},
			"AttestationRewardAccount": {Style: InnerRequired, Kind: InnerString},
			"WasLockingChainSend":      {Style: InnerRequired, Kind: InnerUInt8},
			"Destination":              {Style: InnerOptional, Kind: InnerString},
		},
	},
	"XChainCreateAccountProofSig": {
		Fields: map[string]InnerFieldTemplate{
			"AttestationSignerAccount": {Style: InnerRequired, Kind: InnerString},
			"PublicKey":                {Style: InnerRequired, Kind: InnerString},
			"Amount":                   {Style: InnerRequired},
			"SignatureReward":          {Style: InnerRequired},
			"AttestationRewardAccount": {Style: InnerRequired, Kind: InnerString},
			"WasLockingChainSend":      {Style: InnerRequired, Kind: InnerUInt8},
			"Destination":              {Style: InnerRequired, Kind: InnerString},
		},
	},
	"AuthAccount": {
		Fields: map[string]InnerFieldTemplate{
			"Account": {Style: InnerRequired, Kind: InnerString},
		},
	},
	"PriceData": {
		Fields: map[string]InnerFieldTemplate{
			"BaseAsset":  {Style: InnerRequired, Kind: InnerString},
			"QuoteAsset": {Style: InnerRequired, Kind: InnerString},
			"AssetPrice": {Style: InnerOptional, Kind: InnerUInt64},
			"Scale":      {Style: InnerDefault, Kind: InnerUInt8},
		},
	},
	"Credential": {
		Fields: map[string]InnerFieldTemplate{
			"Issuer":         {Style: InnerRequired, Kind: InnerString},
			"CredentialType": {Style: InnerRequired, Kind: InnerString},
		},
	},
	"Permission": {
		Fields: map[string]InnerFieldTemplate{
			"PermissionValue": {Style: InnerRequired, Kind: InnerPermissionValue},
		},
	},
	"Book": {
		Fields: map[string]InnerFieldTemplate{
			"BookDirectory": {Style: InnerRequired, Kind: InnerString},
			"BookNode":      {Style: InnerRequired, Kind: InnerUInt64},
		},
	},
}

// InnerObjectTemplateByName returns the canonical template for a nested object.
func InnerObjectTemplateByName(name string) (InnerObjectTemplate, bool) {
	template, ok := innerObjectTemplates[name]
	return template, ok
}

// InnerObjectTemplateNames returns all canonical nested object names sorted.
func InnerObjectTemplateNames() []string {
	names := make([]string, 0, len(innerObjectTemplates))
	for name := range innerObjectTemplates {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ArrayElementTemplate returns the nested object type carried by an STArray field.
func ArrayElementTemplate(fieldName string) (string, bool) {
	template, ok := arrayElementTemplates[fieldName]
	return template, ok
}

// ArrayElementTemplates returns a copy of the canonical STArray element map.
func ArrayElementTemplates() map[string]string {
	result := make(map[string]string, len(arrayElementTemplates))
	for fieldName, template := range arrayElementTemplates {
		result[fieldName] = template
	}
	return result
}

// String returns the protocol-oriented name of an inner value kind.
func (k InnerValueKind) String() string {
	switch k {
	case InnerAny:
		return "value"
	case InnerString:
		return "string"
	case InnerUInt8:
		return "UInt8"
	case InnerUInt16:
		return "UInt16"
	case InnerUInt32:
		return "UInt32"
	case InnerUInt64:
		return "UInt64"
	case InnerPermissionValue:
		return "PermissionValue"
	case InnerArray:
		return "array"
	default:
		return "unknown"
	}
}
