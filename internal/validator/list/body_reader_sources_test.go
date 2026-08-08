package list

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSitePollerFetchBoundedHTTPAndFileBodies(t *testing.T) {
	cases := []struct {
		name    string
		bodyLen int
		wantErr bool
	}{
		{name: "http exact", bodyLen: maxBodySize},
		{name: "http overflow", bodyLen: maxBodySize + 1, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := bytes.Repeat([]byte{'x'}, tc.bodyLen)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(payload)
			}))
			defer server.Close()
			poller := newPollerForLifecycleTest(t, server.URL)
			got, err := poller.fetch(t.Context(), server.URL)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected HTTP body limit error")
				}
				return
			}
			if err != nil || len(got) != tc.bodyLen {
				t.Fatalf("HTTP fetch: len=%d err=%v", len(got), err)
			}
		})
	}

	path := filepath.Join(t.TempDir(), "list.json")
	if err := os.WriteFile(path, bytes.Repeat([]byte{'f'}, maxFileBodySize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	poller := newPollerForLifecycleTest(t, "file://"+path)
	if _, err := poller.fetch(t.Context(), "file://"+path); err == nil {
		t.Fatal("expected file body limit error")
	}
}

func TestLoadCacheRejectsOversizeAndTrailingJSON(t *testing.T) {
	key := PublisherKey{0xed, 9}
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{name: "oversize", body: bytes.Repeat([]byte{'x'}, maxFileBodySize+1)},
		{name: "trailing json", body: append([]byte(`{"version":1,"manifest":"x"}`), []byte(" trailing")...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			agg, err := New(Config{PublisherKeys: []PublisherKey{key}})
			if err != nil {
				t.Fatal(err)
			}
			if err := agg.SetCacheDir(dir); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cachePathFor(dir, key), tc.body, 0o600); err != nil {
				t.Fatal(err)
			}
			if got := agg.LoadCache(); got != 0 {
				t.Fatalf("LoadCache accepted invalid body: %d", got)
			}
			var env envelope
			if tc.name == "trailing json" && json.Unmarshal(tc.body, &env) == nil {
				t.Fatal("test fixture unexpectedly accepted trailing JSON")
			}
		})
	}
}
