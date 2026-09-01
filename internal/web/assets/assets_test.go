package assets_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmalkin/westbridge/internal/web/assets"
)

// The frontend is only embedded after `make frontend` has run, so these tests
// describe both worlds: with a build, and on a fresh clone without one.

func TestHandlerWithoutABuildReportsErrNotBuilt(t *testing.T) {
	if assets.Built() {
		t.Skip("a frontend build is embedded; nothing to assert about its absence")
	}

	if _, err := assets.Handler(); err != assets.ErrNotBuilt {
		t.Fatalf("Handler() error = %v, want ErrNotBuilt", err)
	}
}

func TestHandlerServesTheSPA(t *testing.T) {
	if !assets.Built() {
		t.Skip("no frontend build embedded; run `make frontend` to exercise this")
	}

	h, err := assets.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	tests := []struct {
		name, path  string
		wantHTML    bool
		wantStatus  int
		wantContent string
	}{
		{name: "root", path: "/", wantHTML: true, wantStatus: http.StatusOK},
		{name: "deep link", path: "/some/route", wantHTML: true, wantStatus: http.StatusOK},
		{name: "explicit index", path: "/index.html", wantHTML: true, wantStatus: http.StatusOK},
		// A stale index.html referencing a bundle that is no longer embedded
		// must fail loudly. Served the HTML shell instead, the browser
		// reports a module/MIME error that says nothing about the real cause.
		{name: "missing bundle", path: "/assets/index-deadbeef.js", wantStatus: http.StatusNotFound},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantHTML && !strings.Contains(strings.ToLower(rec.Body.String()), "<html") {
				t.Errorf("body for %s is not an HTML document: %.120s", tc.path, rec.Body)
			}
		})
	}
}

func TestHandlerRejectsWrites(t *testing.T) {
	if !assets.Built() {
		t.Skip("no frontend build embedded")
	}

	h, err := assets.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}
