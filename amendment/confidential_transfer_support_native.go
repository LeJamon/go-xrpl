//go:build mptcrypto && cgo

package amendment

import "github.com/LeJamon/go-xrpl/crypto/mptcrypto"

var confidentialTransferBackendAvailable = mptcrypto.Available

var confidentialTransferSupported = confidentialTransferSupport()

func confidentialTransferSupport() Supported {
	if confidentialTransferBackendAvailable() {
		return SupportedYes
	}
	return SupportedNo
}
