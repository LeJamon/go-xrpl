package list

import (
	"bytes"
	"strings"
	"testing"
)

func TestReadBoundedBodyExactAndOverflow(t *testing.T) {
	cases := []struct {
		name    string
		bodyLen int
		wantErr bool
	}{
		{name: "exact limit", bodyLen: maxBodySize},
		{name: "one byte over", bodyLen: maxBodySize + 1, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := readBoundedBody(strings.NewReader(strings.Repeat("x", tc.bodyLen)))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected bounded-reader overflow")
				}
				return
			}
			if err != nil {
				t.Fatalf("readBoundedBody: %v", err)
			}
			if len(body) != tc.bodyLen || !bytes.Equal(body[:1], []byte("x")) {
				t.Fatalf("body length/content: got %d", len(body))
			}
		})
	}
}
