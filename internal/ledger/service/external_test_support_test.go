package service_test

import (
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
)

// progressTimeout bounds waits for something that must happen. A passing test
// returns as soon as it does, so the bound is generous enough for loaded CI.
const progressTimeout = 15 * time.Second

func defaultServiceConfig() service.Config {
	return service.Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
	}
}
