package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The title-detail warning blamed token permissions for every failure. Only a
// 403 is a permission problem; anything else must keep its real cause.
func TestEnrichFleetAppScriptsFailureCause(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		body          string
		wantForbidden bool
		wantErr       bool
	}{
		{name: "forbidden", status: http.StatusForbidden, body: `{}`, wantForbidden: true, wantErr: true},
		// isPermissionError also counts 404, but a missing title is not a
		// permission problem and must not be reported as one.
		{name: "not found", status: http.StatusNotFound, body: `{}`, wantErr: true},
		{name: "server error", status: http.StatusInternalServerError, body: `{"message":"secret-body | [x](http://evil)"}`, wantErr: true},
		{name: "no software package", status: http.StatusOK, body: `{"software_title":{"id":1}}`, wantErr: true},
		{name: "ok", status: http.StatusOK, body: `{"software_title":{"id":1,"software_package":{"categories":["Browsers"]}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer ts.Close()

			apps := []TeamFleetApp{{TitleID: 1, TeamID: 6}}
			testClient(t, ts, "tok").EnrichFleetAppScripts(context.Background(), apps)
			a := apps[0]
			if a.DetailUnavailable != tt.wantErr {
				t.Errorf("DetailUnavailable = %v, want %v", a.DetailUnavailable, tt.wantErr)
			}
			if a.DetailForbidden != tt.wantForbidden {
				t.Errorf("DetailForbidden = %v, want %v", a.DetailForbidden, tt.wantForbidden)
			}
			if (a.DetailError != "") != tt.wantErr {
				t.Errorf("DetailError = %q, want set=%v", a.DetailError, tt.wantErr)
			}
			// DetailError reaches PR comments: no server URL or response body.
			for _, leak := range []string{ts.URL, "secret-body"} {
				if strings.Contains(a.DetailError, leak) {
					t.Errorf("DetailError %q leaks %q", a.DetailError, leak)
				}
			}
		})
	}
}
