package list

import "testing"

func TestNewNormalizesAndValidatesConfig(t *testing.T) {
	key1 := PublisherKey{0xed, 1}
	key2 := PublisherKey{0xed, 2}
	cases := []struct {
		name       string
		cfg        Config
		wantErr    bool
		publishers int
		sites      int
		threshold  int
		static     int
	}{
		{
			name: "deduplicates keys, preserves sites, and defaults threshold",
			cfg: Config{PublisherKeys: []PublisherKey{key1, key1, key2}, SiteURIs: []string{
				"https://one.example", "https://one.example", "file:///tmp/lists.json",
			}, StaticValidatorCount: -3},
			publishers: 2, sites: 3, threshold: 1,
		},
		{
			name: "zero threshold without publishers is valid",
			cfg:  Config{}, threshold: 0,
		},
		{name: "unknown key prefix", cfg: Config{PublisherKeys: []PublisherKey{{1, 1}}}, wantErr: true},
		{name: "negative threshold", cfg: Config{Threshold: -1}, wantErr: true},
		{name: "threshold exceeds normalized publishers", cfg: Config{PublisherKeys: []PublisherKey{key1}, Threshold: 2}, wantErr: true},
		{name: "invalid site URI", cfg: Config{SiteURIs: []string{"file://host/path"}}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agg, err := New(tc.cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected constructor error")
				}
				return
			}
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if got := agg.PublisherCount(); got != tc.publishers {
				t.Fatalf("publishers: got %d want %d", got, tc.publishers)
			}
			if got := len(agg.SiteSnapshot()); got != tc.sites {
				t.Fatalf("sites: got %d want %d", got, tc.sites)
			}
			if got := agg.Threshold(); got != tc.threshold {
				t.Fatalf("threshold: got %d want %d", got, tc.threshold)
			}
			if agg.staticValidatorCount != tc.static {
				t.Fatalf("static count: got %d want %d", agg.staticValidatorCount, tc.static)
			}
		})
	}
}
