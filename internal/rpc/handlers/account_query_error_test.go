package handlers

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
)

func TestMapAccountQueryErr(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		want    int
		message string
	}{
		{"ledger not found", svcerr.ErrLedgerNotFound, rpcerrors.RpcLGR_NOT_FOUND, "ledgerNotFound"},
		{"invalid ledger index", svcerr.ErrInvalidLedgerIndex, rpcerrors.RpcINVALID_PARAMS, "ledgerIndexMalformed"},
		{"invalid ledger hash", svcerr.ErrInvalidLedgerHash, rpcerrors.RpcINVALID_PARAMS, "ledgerHashMalformed"},
		{"account not found", svcerr.ErrAccountNotFound, rpcerrors.RpcACT_NOT_FOUND, "Account not found."},
		{"wrapped account not found", errors.Join(errors.New("query failed"), svcerr.ErrAccountNotFound), rpcerrors.RpcACT_NOT_FOUND, "Account not found."},
		{"invalid marker", svcerr.ErrInvalidMarker, rpcerrors.RpcINVALID_PARAMS, "Invalid field 'marker'."},
		{"internal", errors.New("storage failure"), rpcerrors.RpcINTERNAL, "Internal error."},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := mapAccountQueryErr(test.err, "account query: "+test.err.Error())
			if got.Code != test.want || got.Message != test.message {
				t.Fatalf("mapAccountQueryErr() = %#v, want code %d message %q", got, test.want, test.message)
			}
		})
	}
}

func TestMarkerString(t *testing.T) {
	for _, test := range []struct {
		name   string
		marker json.RawMessage
		want   string
		ok     bool
	}{
		{"absent", nil, "", true},
		{"valid", json.RawMessage(`"opaque"`), "opaque", true},
		{"empty", json.RawMessage(`""`), "", true},
		{"null", json.RawMessage(`null`), "", false},
		{"number", json.RawMessage(`1`), "", false},
		{"object", json.RawMessage(`{}`), "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, rpcErr := markerString(test.marker)
			if test.ok && rpcErr != nil {
				t.Fatalf("markerString() error = %v", rpcErr)
			}
			if !test.ok && (rpcErr == nil || rpcErr.Message != "Invalid field 'marker', not string.") {
				t.Fatalf("markerString() error = %#v", rpcErr)
			}
			if got != test.want {
				t.Fatalf("markerString() = %q, want %q", got, test.want)
			}
		})
	}
}
