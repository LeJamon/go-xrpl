package amendment

import "github.com/LeJamon/go-xrpl/crypto/mptcrypto"

func confidentialTransferSupport() Supported {
	if mptcrypto.Available() {
		return SupportedYes
	}
	return SupportedNo
}
