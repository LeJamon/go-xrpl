package node

import (
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/adaptor"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
)

type watchdogEngine struct {
	consensus.Engine
	mu   sync.Mutex
	ping func()
}

func (e *watchdogEngine) SetStallPing(ping func()) {
	e.mu.Lock()
	e.ping = ping
	e.mu.Unlock()
}

func (e *watchdogEngine) hasPing() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ping != nil
}

type engineWithoutWatchdog struct {
	consensus.Engine
}

func TestConfigureWatchdogModeGate(t *testing.T) {
	tests := []struct {
		name       string
		standalone bool
		config     config.WatchdogConfig
	}{
		{name: "standalone", standalone: true},
		{name: "disabled", config: config.WatchdogConfig{Disabled: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := &nodeRuntime{
				appConfig:  &config.Config{Watchdog: test.config},
				standalone: test.standalone,
			}
			if err := runtime.configureWatchdog(); err != nil {
				t.Fatalf("configure watchdog: %v", err)
			}
			if runtime.watchdog != nil || runtime.stopWatchdog != nil {
				t.Fatal("inert mode configured a watchdog")
			}
		})
	}
}

func TestConfigureWatchdogFailsClosedWithoutHeartbeat(t *testing.T) {
	tests := []struct {
		name      string
		consensus *adaptor.Components
		config    config.WatchdogConfig
	}{
		{name: "nil components"},
		{name: "nil engine", consensus: &adaptor.Components{}},
		{name: "unsupported engine", consensus: &adaptor.Components{Engine: &engineWithoutWatchdog{}}},
		{
			name:      "invalid thresholds",
			consensus: &adaptor.Components{Engine: &watchdogEngine{}},
			config:    config.WatchdogConfig{WarnSeconds: 100, FatalSeconds: 10, AbortSeconds: 600},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := &nodeRuntime{
				appConfig: &config.Config{Watchdog: test.config},
				consensus: test.consensus,
			}
			if err := runtime.configureWatchdog(); err == nil {
				t.Fatal("enabled watchdog accepted missing or invalid heartbeat wiring")
			}
			if runtime.watchdog != nil || runtime.stopWatchdog != nil {
				t.Fatal("failed setup retained a watchdog")
			}
		})
	}
}

func TestConfigureWatchdogAttachesAndDetachesHeartbeat(t *testing.T) {
	engine := &watchdogEngine{}
	runtime := &nodeRuntime{
		appConfig: &config.Config{},
		consensus: &adaptor.Components{Engine: engine},
	}
	if err := runtime.configureWatchdog(); err != nil {
		t.Fatalf("configure watchdog: %v", err)
	}
	if runtime.watchdog == nil || runtime.stopWatchdog == nil || !engine.hasPing() {
		t.Fatal("watchdog heartbeat was not fully attached")
	}
	if err := runtime.watchdog.Start(t.Context()); err != nil {
		t.Fatalf("start watchdog: %v", err)
	}
	runtime.stopWatchdog()
	if engine.hasPing() {
		t.Fatal("watchdog heartbeat remained attached after stop")
	}
	if err := runtime.watchdog.Start(t.Context()); err == nil {
		t.Fatal("closed heartbeat registration allowed a restart")
	}
}

func TestConfigureWatchdogAcceptsRCLEngine(t *testing.T) {
	runtime := &nodeRuntime{
		appConfig: &config.Config{},
		consensus: &adaptor.Components{Engine: &rcl.Engine{}},
	}
	if err := runtime.configureWatchdog(); err != nil {
		t.Fatalf("configure watchdog with RCL engine: %v", err)
	}
	runtime.stopWatchdog()
}
