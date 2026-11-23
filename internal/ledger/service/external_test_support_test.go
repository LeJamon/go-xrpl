package service_test

import (
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
)

const progressTimeout = 15 * time.Second

func defaultServiceConfig() service.Config {
	return service.Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
	}
}
