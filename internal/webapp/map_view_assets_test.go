package webapp

import (
	"bytes"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

func TestMapViewAssetsShellAndEmbeddedHTTPResponses(t *testing.T) {
	c := &client{app: testApp(t, true)}
	page := c.call(http.MethodGet, "/", nil)
	pageType, _, err := mime.ParseMediaType(page.Header().Get("Content-Type"))
	if page.Code != http.StatusOK || err != nil || pageType != "text/html" {
		t.Fatalf("shell: status=%d content-type=%q error=%v", page.Code, page.Header().Get("Content-Type"), err)
	}
	html := page.Body.String()
	appScript := strings.Index(html, `src="/assets/app.js"`)
	if appScript < 0 {
		t.Fatal("shell does not include app.js")
	}
	for _, asset := range []struct {
		name, mediaType, marker string
	}{
		{"map-structured-view.js", "text/javascript", "installMemMapStructuredView"},
		{"map-navigation.js", "text/javascript", "installMemMapNavigation"},
		{"map-from-sources.js", "text/javascript", "installMemMapFromSources"},
		{"map-view-presets.js", "text/javascript", "installMemMapViewPresets"},
		{"map-view-controls.js", "text/javascript", "installMemMapViewControls"},
		{"map-view-export.js", "text/javascript", "installMemMapViewExport"},
		{"map-structured-view.css", "text/css", ".map-structured-view"},
	} {
		t.Run(asset.name, func(t *testing.T) {
			path := "/assets/" + asset.name
			pattern := `<script\b[^>]*\bsrc="` + regexp.QuoteMeta(path) + `"[^>]*>`
			if asset.mediaType == "text/css" {
				pattern = `<link\b[^>]*\bhref="` + regexp.QuoteMeta(path) + `"[^>]*>`
			}
			tags := regexp.MustCompile(pattern).FindAllString(html, -1)
			if len(tags) != 1 {
				t.Fatalf("shell must reference %s exactly once, got %d", path, len(tags))
			}
			if asset.mediaType == "text/javascript" && (strings.Index(html, tags[0]) >= appScript || !strings.Contains(tags[0], "defer")) {
				t.Fatalf("script must load with defer before app.js: %s", tags[0])
			}
			if asset.mediaType == "text/css" && !strings.Contains(tags[0], `rel="stylesheet"`) {
				t.Fatalf("CSS is not linked as a stylesheet: %s", tags[0])
			}
			expected, err := assets.ReadFile("assets/" + asset.name)
			if err != nil || len(expected) == 0 || !bytes.Contains(expected, []byte(asset.marker)) {
				t.Fatalf("missing or wrong embedded asset %s: %v", path, err)
			}
			response := c.call(http.MethodGet, path, nil)
			mediaType, params, err := mime.ParseMediaType(response.Header().Get("Content-Type"))
			if response.Code != http.StatusOK || err != nil || mediaType != asset.mediaType || params["charset"] != "utf-8" {
				t.Fatalf("GET %s: status=%d content-type=%q error=%v", path, response.Code, response.Header().Get("Content-Type"), err)
			}
			if !bytes.Equal(response.Body.Bytes(), expected) {
				t.Fatalf("GET %s did not return its exact embedded content", path)
			}
		})
	}
}
