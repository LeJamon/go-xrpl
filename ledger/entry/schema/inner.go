package schema

import "sort"

// InnerFieldStyle describes the serialization presence rule for a field in an
// embedded STObject.
type InnerFieldStyle uint8

const (
	InnerRequired InnerFieldStyle = iota
	InnerOptional
	InnerDefault
)

// InnerValueKind describes the JSON representation accepted by an embedded
// STObject field. The ledger-entry decoder and the generated typed wrappers
// share this schema.
type InnerValueKind uint8

const (
	InnerAny InnerValueKind = iota
	InnerString
	InnerUInt8
	InnerUInt16
	InnerUInt32
	InnerUInt64
	InnerPermissionValue
	InnerArray
)

type InnerFieldTemplate struct {
	Style InnerFieldStyle
	Kind  InnerValueKind
}

type InnerObjectTemplate struct {
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

func InnerObjectTemplateByName(name string) (InnerObjectTemplate, bool) {
	template, ok := innerObjectTemplates[name]
	return template, ok
}

func InnerObjectTemplateNames() []string {
	names := make([]string, 0, len(innerObjectTemplates))
	for name := range innerObjectTemplates {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func ArrayElementTemplate(fieldName string) (string, bool) {
	template, ok := arrayElementTemplates[fieldName]
	return template, ok
}

func ArrayElementTemplates() map[string]string {
	result := make(map[string]string, len(arrayElementTemplates))
	for fieldName, template := range arrayElementTemplates {
		result[fieldName] = template
	}
	return result
}

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
