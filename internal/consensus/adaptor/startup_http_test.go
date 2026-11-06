package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/stretchr/testify/require"
)

func TestOverlayOptionsPeerHTTP(t *testing.T) {
	on, off := true, false
	for _, tc := range []struct {
		name string
		app  config.Config
		want peermanagement.HTTPConfig
	}{
		{
			name: "defaults",
			want: peermanagement.HTTPConfig{CrawlOverlay: true, CrawlServer: true, CrawlUNL: true, ValidatorList: true},
		},
		{
			name: "individual sections",
			app: config.Config{
				Crawl: config.CrawlConfig{Overlay: &off, Server: &off, Counts: &on, UNL: &off},
				VL:    config.ValidatorListHTTPConfig{Enabled: &off},
			},
			want: peermanagement.HTTPConfig{CrawlCounts: true},
		},
		{
			name: "disabled",
			app: config.Config{
				Crawl: config.CrawlConfig{Enabled: &off, Overlay: &on, Server: &on, Counts: &on, UNL: &on},
				VL:    config.ValidatorListHTTPConfig{Enabled: &off},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := peermanagement.DefaultConfig()
			for _, option := range OverlayOptionsFromConfig(&tc.app) {
				option(&cfg)
			}
			require.Equal(t, tc.want, cfg.HTTP)
		})
	}
}
