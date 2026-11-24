package txq

import (
	"os"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx/all"
)

func TestMain(m *testing.M) {
	all.RegisterAll()
	os.Exit(m.Run())
}
