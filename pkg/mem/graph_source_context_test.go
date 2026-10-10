package mem

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestKnowledgeSourceContextHTTPAuthorizationAndRevision(t *testing.T) {
	store, anchor, _ := knowledgeMapSourceFixture(t, ".md")
	defer store.Close()
	token := "source-context-test-token"
	handler := NewKnowledgeMapWorkspaceHandler(store, "test", token, "")
	call := func(cookie bool, site, citation, method string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1/api/source?format=text&citation="+url.QueryEscape(citation), nil)
		if cookie {
			r.AddCookie(knowledgeMapSourceCookie(token))
		}
		r.Header.Set("Sec-Fetch-Site", site)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		cookie bool
		site   string
		want   int
	}{{false, "same-origin", 403}, {true, "cross-site", 403}, {true, "same-origin", 200}} {
		w := call(tc.cookie, tc.site, anchor.CitationID, "GET")
		if w.Code != tc.want {
			t.Fatalf("auth %+v: %d %s", tc, w.Code, w.Body.String())
		}
		if w.Code == 200 {
			var result knowledgeMapSourceText
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Anchor.CitationID != anchor.CitationID || result.Text == "" {
				t.Fatalf("context: %+v %v", result, err)
			}
		}
	}
	if got := call(true, "same-origin", anchor.CitationID, "HEAD"); got.Code != http.StatusOK || got.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %s", got.Code, got.Body.String())
	}
	if got := call(true, "same-origin", "../../config.json", "GET"); got.Code != 404 {
		t.Fatalf("arbitrary path: %d", got.Code)
	}
	if _, err := store.db.Exec(`UPDATE entries SET document_revision=? WHERE document_id=?`, ChunkContentHash("new revision"), anchor.DocumentID); err != nil {
		t.Fatal(err)
	}
	if got := call(true, "same-origin", anchor.CitationID, "GET"); got.Code != 409 {
		t.Fatalf("stale context: %d %s", got.Code, got.Body.String())
	}
}
