package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedWorkspaceAssets(t *testing.T) {
	srv, _, eng := setupTestServer(t)
	defer eng.Close()
	for _, asset := range []struct {
		path, contentType, marker string
	}{
		{"/assets/redesign.css", "text/css", ".workspace"},
		{"/assets/workspace.js", "text/javascript", "function requestAPI"},
	} {
		t.Run(asset.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			srv.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", asset.path, nil))
			if recorder.Code != 200 || !strings.Contains(recorder.Header().Get("Content-Type"), asset.contentType) || !strings.Contains(recorder.Body.String(), asset.marker) {
				t.Fatalf("asset not served correctly: status=%d content-type=%s", recorder.Code, recorder.Header().Get("Content-Type"))
			}
		})
	}
}
