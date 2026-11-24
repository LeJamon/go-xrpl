package pathfind

import (
	"bytes"
	"encoding/json"
	"slices"

	binarytypes "github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	rpctypes "github.com/LeJamon/go-xrpl/internal/rpc/types"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/payment/pathfinder"
	"github.com/LeJamon/go-xrpl/keylet"
)

const maxSourceCurrencies = 18

// ParseSourceCurrencies parses the optional source_currencies field shared by
// the legacy HTTP and persistent WebSocket path-find requests.
func ParseSourceCurrencies(raw json.RawMessage, srcAccount [20]byte, sendMax *tx.Amount) ([]payment.Issue, *rpcerrors.RpcError) {
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil || len(entries) == 0 || len(entries) > maxSourceCurrencies {
		return nil, rpcerrors.RpcErrorSrcCurMalformed("Source currency is malformed.")
	}

	var sendMaxIssue payment.Issue
	if sendMax != nil {
		sendMaxIssue = payment.GetIssue(*sendMax)
	}

	var sourceCurrencies []payment.Issue

	for _, rawEntry := range entries {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rawEntry, &fields); err != nil || fields == nil {
			return nil, rpcerrors.RpcErrorSrcCurMalformed("Source currency is malformed.")
		}

		rawCurrency, hasCurrency := fields["currency"]
		rawMPT, hasMPT := fields["mpt_issuance_id"]
		_, hasIssuer := fields["issuer"]

		// validJSONAsset rejects an MPT carrying either currency or issuer
		// before parsing the asset or its optional issuer.
		if hasCurrency == hasMPT || (hasMPT && hasIssuer) {
			return nil, rpcerrors.RpcErrorSrcCurMalformed("Source currency is malformed.")
		}

		if hasMPT {
			mptValue, ok := rpctypes.AssetString(rawMPT)
			if !ok {
				return nil, rpcerrors.RpcErrorSrcCurMalformed("Source currency is malformed.")
			}
			mptID, ok := pathfinder.ParseSourceMPTID(mptValue)
			if !ok {
				return nil, rpcerrors.RpcErrorSrcCurMalformed("Source currency is malformed.")
			}
			issue := payment.NewMPTIssue(mptID)
			if sendMax == nil || issue.Equal(sendMaxIssue) {
				sourceCurrencies = append(sourceCurrencies, issue)
			}
			continue
		}

		currencyValue, ok := rpctypes.AssetString(rawCurrency)
		if !ok || !keylet.IsValidCurrencyCode(currencyValue) {
			return nil, rpcerrors.RpcErrorSrcCurMalformed("Source currency is malformed.")
		}
		currencyBytes := keylet.CurrencyBytes(currencyValue)
		currency, err := binarytypes.DecodeCurrencyCode(currencyBytes[:])
		if err != nil {
			return nil, rpcerrors.RpcErrorSrcCurMalformed("Source currency is malformed.")
		}

		var issuer [20]byte
		if rawIssuer, hasIssuer := fields["issuer"]; hasIssuer {
			issuerValue, ok := rpctypes.AssetString(rawIssuer)
			if !ok {
				return nil, rpcerrors.RpcErrorSrcIsrMalformed("Source issuer is malformed.")
			}
			issuer, ok = rpctypes.ParseIssuer(issuerValue)
			if !ok {
				return nil, rpcerrors.RpcErrorSrcIsrMalformed("Source issuer is malformed.")
			}
		}

		if currency == "XRP" {
			if issuer != [20]byte{} {
				return nil, rpcerrors.RpcErrorSrcCurMalformed("Source currency is malformed.")
			}
		} else if issuer == [20]byte{} {
			issuer = srcAccount
		}

		if sendMax != nil {
			if sendMaxIssue.IsMPT || currencyBytes != keylet.CurrencyBytes(sendMaxIssue.Currency) {
				continue
			}
			if issuer != srcAccount && sendMaxIssue.Issuer != srcAccount && issuer != sendMaxIssue.Issuer {
				return nil, rpcerrors.RpcErrorSrcIsrMalformed("Source issuer is malformed.")
			}
			if issuer == srcAccount {
				issuer = sendMaxIssue.Issuer
			}
		}

		sourceCurrencies = append(sourceCurrencies, payment.Issue{Currency: currency, Issuer: issuer})
	}

	slices.SortFunc(sourceCurrencies, compareIssues)
	return slices.Compact(sourceCurrencies), nil
}

func compareIssues(a, b payment.Issue) int {
	if a.IsMPT {
		if b.IsMPT {
			return bytes.Compare(a.MPTID[:], b.MPTID[:])
		}
		return -1
	}
	if b.IsMPT {
		return 1
	}
	aCurrency := keylet.CurrencyBytes(a.Currency)
	bCurrency := keylet.CurrencyBytes(b.Currency)
	if cmp := bytes.Compare(aCurrency[:], bCurrency[:]); cmp != 0 {
		return cmp
	}
	return bytes.Compare(a.Issuer[:], b.Issuer[:])
}
