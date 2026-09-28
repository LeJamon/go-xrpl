package observability

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPProfHandlerRejectsFullGoroutineStacks(t *testing.T) {
	t.Parallel()
	for _, query := range []string{"debug=2", "debug=3", "debug=02", "debug=%2B2", "debug=2&seconds=1", "debug=2&debug=1"} {
		t.Run(query, func(t *testing.T) {
			response := httptest.NewRecorder()
			PProfHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/debug/pprof/goroutine?"+query, nil))
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "use debug=1") {
				t.Fatalf("unsafe stack request: status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestPProfHandlerLimitsGoroutineRequestBody(t *testing.T) {
	t.Parallel()
	padding := strings.Repeat("x", 2*maxPProfRequestBodyBytes)
	for _, tc := range []struct {
		name, contentType, body string
	}{
		{"urlencoded", "application/x-www-form-urlencoded", "padding=" + padding},
		{"multipart", "multipart/form-data; boundary=profile", "--profile\r\nContent-Disposition: form-data; name=\"padding\"\r\n\r\n" + padding + "\r\n--profile--\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &countingProfileReader{Reader: strings.NewReader(tc.body)}
			request := httptest.NewRequest(http.MethodPost, "/debug/pprof/goroutine?debug=1", body)
			request.Header.Set("Content-Type", tc.contentType)
			response := httptest.NewRecorder()
			PProfHandler().ServeHTTP(response, request)
			if body.read > maxPProfRequestBodyBytes+1 {
				t.Fatalf("read %d request bytes, limit is %d", body.read, maxPProfRequestBodyBytes)
			}
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "goroutine profile: total") {
				t.Fatalf("grouped profile: status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

type countingProfileReader struct {
	io.Reader
	read int
}

func (r *countingProfileReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}

func TestPProfHandlerPreservesGroupedAndBinaryGoroutineProfiles(t *testing.T) {
	t.Parallel()
	t.Run("grouped text", func(t *testing.T) {
		response := httptest.NewRecorder()
		PProfHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/debug/pprof/goroutine?debug=1", nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "goroutine profile: total") {
			t.Fatalf("grouped profile: status=%d body=%s", response.Code, response.Body.String())
		}
	})
	t.Run("binary", func(t *testing.T) {
		response := httptest.NewRecorder()
		PProfHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/debug/pprof/goroutine", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("binary profile: status=%d", response.Code)
		}
		reader, err := gzip.NewReader(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		data, err := io.ReadAll(reader)
		if err != nil || len(data) == 0 {
			t.Fatalf("binary profile: bytes=%d err=%v", len(data), err)
		}
	})
}

func TestPProfHandler(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	response := httptest.NewRecorder()
	PProfHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if !strings.Contains(response.Body.String(), "profile") {
		t.Fatalf("body does not contain pprof index:\n%s", response.Body.String())
	}
}
