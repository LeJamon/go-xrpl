package service_test

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
)

func defaultServiceConfig() service.Config {
	return service.Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
	}
}
