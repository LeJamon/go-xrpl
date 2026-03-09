//revive:disable:var-naming
package types

import "errors"

var (
	errNotValidJSON         = errors.New("not a valid json")
	errDecodeClassicAddress = errors.New("unable to decode classic address")
	errReadBytes            = errors.New("read bytes error")
)
