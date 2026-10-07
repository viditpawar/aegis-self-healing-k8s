package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	cases := []struct {
		path     string
		wantBody string
	}{
		{"/healthz", "ok"},
		{"/metrics", "aegis_crashloop_deletions_total"},
		{"/metrics", "aegis_pending_deletions_total"},
		{"/metrics", "aegis_leader"},
	}

	for _, tc := range cases {
		t.Run(tc.path+" contains "+tc.wantBody, func(t *testing.T) {
			resp, err := http.Get(srv.URL + tc.path)
			if err != nil {
				t.Fatalf("GET %s: %v", tc.path, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Errorf("status = %d, want 200", resp.StatusCode)
			}
			body, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(body), tc.wantBody) {
				t.Errorf("body does not contain %q", tc.wantBody)
			}
		})
	}
}
