package unsafeindirectmutation

import "github.com/LeJamon/go-xrpl/internal/rpc/handlers/testdata/internal_error_audit/unsafe_indirect_mutation/bridge"

func mutate() {
	err := bridge.Error()
	err.Message = "private detail"
}
