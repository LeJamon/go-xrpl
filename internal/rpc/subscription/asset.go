package subscription

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	codecTypes "github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/keylet"
)

type assetKind uint8

const (
	assetIssue assetKind = iota
	assetMPT
)

type asset struct {
	kind     assetKind
	currency [20]byte
	issuer   [20]byte
	mpt      [24]byte
}

type book struct {
	takerPays     asset
	takerGets     asset
	domain        [32]byte
	domainPresent bool
}

func (b book) reversed() book {
	b.takerPays, b.takerGets = b.takerGets, b.takerPays
	return b
}

func SnapshotBook(request types.BookRequest) (takerPays, takerGets types.Amount, domain string, rpcErr *types.RpcError) {
	book, rpcErr := parseBookRequest(request, true)
	if rpcErr != nil {
		return types.Amount{}, types.Amount{}, "", rpcErr
	}
	takerPays, ok := book.takerPays.amount()
	if !ok {
		return types.Amount{}, types.Amount{}, "", types.RpcErrorInternal()
	}
	takerGets, ok = book.takerGets.amount()
	if !ok {
		return types.Amount{}, types.Amount{}, "", types.RpcErrorInternal()
	}
	if book.domainPresent {
		domain = strings.ToUpper(hex.EncodeToString(book.domain[:]))
	}
	return takerPays, takerGets, domain, nil
}

func (asset asset) amount() (types.Amount, bool) {
	if asset.kind == assetMPT {
		return types.Amount{MPTIssuanceID: strings.ToUpper(hex.EncodeToString(asset.mpt[:]))}, true
	}
	currency, err := codecTypes.DecodeCurrencyCode(asset.currency[:])
	if err != nil {
		return types.Amount{}, false
	}
	amount := types.Amount{Currency: currency}
	if asset.currency == [20]byte{} {
		return amount, true
	}
	issuer, err := addresscodec.EncodeAccountIDToClassicAddress(asset.issuer[:])
	if err != nil {
		return types.Amount{}, false
	}
	amount.Issuer = issuer
	return amount, true
}

func parseBookRequest(request types.BookRequest, includeTaker bool) (book, *types.RpcError) {
	if _, rpcErr := bookSideObject(request.TakerPays); rpcErr != nil {
		return book{}, rpcErr
	}
	if _, rpcErr := bookSideObject(request.TakerGets); rpcErr != nil {
		return book{}, rpcErr
	}
	pays, rpcErr := parseAsset(request.TakerPays, true)
	if rpcErr != nil {
		return book{}, rpcErr
	}
	gets, rpcErr := parseAsset(request.TakerGets, false)
	if rpcErr != nil {
		return book{}, rpcErr
	}
	if pays == gets {
		return book{}, types.RpcErrorBadMarket()
	}

	wire, wireDecoded := request.Wire()
	if includeTaker {
		if wireDecoded && wire.Taker != nil {
			var taker string
			if json.Unmarshal(wire.Taker, &taker) != nil || taker == "" || !isValidXRPLAddress(taker) {
				return book{}, types.RpcErrorActMalformed("Account malformed.")
			}
		} else if request.Taker != "" && !isValidXRPLAddress(request.Taker) {
			return book{}, types.RpcErrorActMalformed("Account malformed.")
		}
	}

	canonicalBook := book{takerPays: pays, takerGets: gets}
	if wireDecoded && wire.Domain != nil {
		var domain string
		if json.Unmarshal(wire.Domain, &domain) != nil || domain == "" || !parseDomain(domain, &canonicalBook.domain) {
			return book{}, types.RpcErrorDomainMalformed("")
		}
		canonicalBook.domainPresent = true
	} else if request.Domain != "" {
		if !parseDomain(request.Domain, &canonicalBook.domain) {
			return book{}, types.RpcErrorDomainMalformed("")
		}
		canonicalBook.domainPresent = true
	}
	return canonicalBook, nil
}

func parseAsset(raw json.RawMessage, isPays bool) (asset, *types.RpcError) {
	assetErr := func() *types.RpcError {
		if isPays {
			return types.RpcErrorSrcCurMalformed("Source currency is malformed.")
		}
		return types.RpcErrorDstAmtMalformed("Destination amount/currency/issuer is malformed.")
	}
	issuerErr := func() *types.RpcError {
		if isPays {
			return types.RpcErrorSrcIsrMalformed("Source issuer is malformed.")
		}
		return types.RpcErrorDstIsrMalformed("Destination issuer is malformed.")
	}

	var side map[string]json.RawMessage
	if raw == nil || json.Unmarshal(raw, &side) != nil {
		return asset{}, types.RpcErrorInvalidParams("Invalid parameters.")
	}
	rawMPT, hasMPT := side["mpt_issuance_id"]
	rawCurrency, hasCurrency := side["currency"]
	rawIssuer, hasIssuer := side["issuer"]
	if hasMPT && (hasCurrency || hasIssuer) {
		return asset{}, types.RpcErrorInvalidParams("Invalid parameters.")
	}
	if hasCurrency {
		currency, ok := jsonStringLike(rawCurrency)
		if currency == "0" {
			currency = ""
		}
		if !ok || !keylet.IsValidCurrencyCode(currency) {
			return asset{}, assetErr()
		}
		canonicalAsset := asset{kind: assetIssue, currency: keylet.CurrencyBytes(currency)}
		if hasIssuer {
			var issuer string
			if json.Unmarshal(rawIssuer, &issuer) != nil {
				return asset{}, issuerErr()
			}
			issuerID, ok := parseIssuer(issuer)
			if !ok {
				return asset{}, issuerErr()
			}
			canonicalAsset.issuer = issuerID
			if canonicalAsset.issuer == noAccountID {
				return asset{}, issuerErr()
			}
		}
		isXRP := canonicalAsset.currency == [20]byte{}
		isXRPIssuer := !hasIssuer || canonicalAsset.issuer == xrpAccountID
		if isXRP != isXRPIssuer {
			return asset{}, issuerErr()
		}
		return canonicalAsset, nil
	}
	if hasMPT {
		value, ok := jsonStringLike(rawMPT)
		if !ok {
			return asset{}, assetErr()
		}
		var id [24]byte
		if value != "0" {
			decoded, err := hex.DecodeString(value)
			if err != nil || len(decoded) != len(id) {
				return asset{}, assetErr()
			}
			copy(id[:], decoded)
		}
		return asset{kind: assetMPT, mpt: id}, nil
	}
	return asset{}, assetErr()
}

func jsonStringLike(raw json.RawMessage) (string, bool) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return "", false
	}
	switch v := value.(type) {
	case nil:
		return "", true
	case string:
		return v, true
	case bool:
		return strconv.FormatBool(v), true
	case json.Number:
		number := v.String()
		if strings.ContainsAny(number, ".eE") {
			real, err := strconv.ParseFloat(number, 64)
			if err != nil {
				return "", false
			}
			return strconv.FormatFloat(real, 'f', 6, 64), true
		}
		if strings.HasPrefix(number, "-") {
			integer, err := strconv.ParseInt(number, 10, 64)
			if err != nil {
				return "", false
			}
			return strconv.FormatInt(integer, 10), true
		}
		integer, err := strconv.ParseUint(number, 10, 64)
		if err != nil {
			return "", false
		}
		return strconv.FormatUint(integer, 10), true
	default:
		return "", false
	}
}

func parseIssuer(value string) ([20]byte, bool) {
	var issuer [20]byte
	if value == "0" {
		return issuer, true
	}
	if decoded, err := hex.DecodeString(value); err == nil && len(decoded) == len(issuer) {
		copy(issuer[:], decoded)
		return issuer, true
	}
	_, decoded, err := addresscodec.DecodeClassicAddressToAccountID(value)
	if err != nil {
		return [20]byte{}, false
	}
	copy(issuer[:], decoded)
	return issuer, true
}

func parseDomain(value string, domain *[32]byte) bool {
	if value == "0" {
		*domain = [32]byte{}
		return true
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(domain) {
		return false
	}
	copy(domain[:], decoded)
	return true
}

func bookFromSpec(spec types.OrderBookSpec) (book, bool) {
	pays, ok := assetFromSpec(spec.TakerPays)
	if !ok {
		return book{}, false
	}
	gets, ok := assetFromSpec(spec.TakerGets)
	if !ok {
		return book{}, false
	}
	canonicalBook := book{takerPays: pays, takerGets: gets}
	if spec.Domain != "" {
		if !parseDomain(spec.Domain, &canonicalBook.domain) {
			return book{}, false
		}
		canonicalBook.domainPresent = true
	}
	return canonicalBook, true
}

func assetFromSpec(spec types.CurrencySpec) (asset, bool) {
	if spec.MPTIssuanceID != "" {
		if spec.Currency != "" || spec.Issuer != "" {
			return asset{}, false
		}
		var id [24]byte
		if spec.MPTIssuanceID != "0" {
			decoded, err := hex.DecodeString(spec.MPTIssuanceID)
			if err != nil || len(decoded) != len(id) {
				return asset{}, false
			}
			copy(id[:], decoded)
		}
		return asset{kind: assetMPT, mpt: id}, true
	}
	currency := spec.Currency
	if currency == "0" {
		currency = ""
	}
	if !keylet.IsValidCurrencyCode(currency) {
		return asset{}, false
	}
	canonicalAsset := asset{kind: assetIssue, currency: keylet.CurrencyBytes(currency)}
	if spec.Issuer != "" {
		issuer, ok := parseIssuer(spec.Issuer)
		if !ok {
			return asset{}, false
		}
		canonicalAsset.issuer = issuer
		if canonicalAsset.issuer == noAccountID {
			return asset{}, false
		}
	}
	isXRP := canonicalAsset.currency == [20]byte{}
	isXRPIssuer := spec.Issuer == "" || canonicalAsset.issuer == xrpAccountID
	return canonicalAsset, isXRP == isXRPIssuer
}
