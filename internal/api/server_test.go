package api

import (
	"io/fs"
	"net/http"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestParseZoom(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		fallback int
		want     int
		wantErr  bool
	}{
		{"empty uses default", "", 14, 14, false},
		{"min", "0", 14, 0, false},
		{"max", "16", 0, 16, false},
		{"trim", " 7 ", 0, 7, false},
		{"not an int", "abc", 0, 0, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseZoom(tc.raw, tc.fallback)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseZoom(%q) error = %v, wantErr %v", tc.raw, err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("parseZoom(%q) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseRangeHeader(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		size    int64
		start   int64
		end     int64
		partial bool
		wantErr bool
	}{
		{"full", "", 100, 0, 99, false, false},
		{"prefix", "bytes=0-9", 100, 0, 9, true, false},
		{"open ended", "bytes=90-", 100, 90, 99, true, false},
		{"suffix", "bytes=-10", 100, 90, 99, true, false},
		{"clamp end", "bytes=90-200", 100, 90, 99, true, false},
		{"suffix larger than size", "bytes=-200", 100, 0, 99, true, false},
		{"invalid start", "bytes=100-", 100, 0, 0, false, true},
		{"invalid order", "bytes=20-10", 100, 0, 0, false, true},
		{"multiple ranges", "bytes=0-1,3-4", 100, 0, 0, false, true},
		{"bad unit", "items=0-1", 100, 0, 0, false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end, partial, err := parseRangeHeader(tc.raw, tc.size)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseRangeHeader(%q) error = %v, wantErr %v", tc.raw, err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if start != tc.start || end != tc.end || partial != tc.partial {
				t.Fatalf("parseRangeHeader(%q) = (%d,%d,%v), want (%d,%d,%v)",
					tc.raw, start, end, partial, tc.start, tc.end, tc.partial)
			}
		})
	}
}

func TestProxyRouteRegistered(t *testing.T) {
	e := echo.New()
	s := &Server{}
	s.registerRoutes(e)

	for _, route := range e.Routes() {
		if route.Method == http.MethodGet && route.Path == "/tiles/:id/proxy" {
			return
		}
	}
	t.Fatal("GET /tiles/:id/proxy is not registered")
}

func TestParseTilingInputs(t *testing.T) {
	if got, err := parseSimplifyFactor(""); err != nil || got != 1.0 {
		t.Fatalf("default simplify factor = %v, %v", got, err)
	}
	if got, err := parseSimplifyFactor("0.1"); err != nil || got != 0.1 {
		t.Fatalf("simplify factor 0.1 = %v, %v", got, err)
	}
	if _, err := parseSimplifyFactor("0"); err == nil {
		t.Fatal("simplify factor 0 should be rejected")
	}

	if got, err := parseBool("", false); err != nil || got {
		t.Fatalf("default bool = %v, %v", got, err)
	}
	if got, err := parseBool("true", false); err != nil || !got {
		t.Fatalf("bool true = %v, %v", got, err)
	}
	if _, err := parseBool("nope", false); err == nil {
		t.Fatal("invalid bool should be rejected")
	}

	got := parsePropertyList(" name, pop,name, , owner ")
	want := []string{"name", "pop", "owner"}
	if len(got) != len(want) {
		t.Fatalf("properties = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("properties = %v, want %v", got, want)
		}
	}
}

func TestPrivacyRouteRegistered(t *testing.T) {
	e := echo.New()
	s := &Server{}
	s.registerRoutes(e)

	for _, route := range e.Routes() {
		if route.Method == http.MethodGet && route.Path == "/privacy" {
			return
		}
	}
	t.Fatal("GET /privacy is not registered")
}

func TestPrivacyPageEmbedded(t *testing.T) {
	data, err := fs.ReadFile(staticFS(), "privacy.html")
	if err != nil {
		t.Fatalf("privacy.html is not embedded: %v", err)
	}
	if !strings.Contains(string(data), "Privacy Policy") {
		t.Fatal("privacy.html does not contain the policy title")
	}
}
