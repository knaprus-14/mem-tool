package mem

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/sourcefile"
	"github.com/knaprus-14/mem-tool/pkg/ingest"
)

func TestKnowledgeSourcePinsSelectCurrentEditionWithoutRebindingOldEvidence(t *testing.T) {
	store, old, source := knowledgeMapPDFSourceFixture(t)
	defer store.Close()
	newBytes := []byte("%PDF-1.4\nsecond physical edition\n%%EOF\n")
	if err := os.WriteFile(source, newBytes, 0600); err != nil {
		t.Fatal(err)
	}
	fileHash, err := sourcefile.Hash(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	chunks := validStructuredChunks()
	for i := range chunks {
		chunks[i].Text += " New edition."
		chunks[i].Provenance.SourcePath = source
		chunks[i].Provenance.Page = old.Page
		chunks[i].Provenance.SourceFileHash = fileHash
		chunks[i].Provenance.DocumentRevision = ChunkContentHash("second physical edition")
		chunks[i].Provenance.ChunkHash = ChunkContentHash(chunks[i].Text)
	}
	if err = store.ReplaceDocumentChunks(source, chunks); err != nil {
		t.Fatal(err)
	}
	entry := store.GetBySourceFile(source)[0]
	current, err := EvidenceAnchorForEntry(entry, entry.Text)
	if err != nil {
		t.Fatal(err)
	}
	if current.CitationID != old.CitationID {
		t.Fatal("fixture must preserve source coordinates")
	}
	if err = store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{
		ID: "source-node-current", Kind: KnowledgeNodeClaim, Origin: KnowledgeOriginGenerated,
		Status: KnowledgeStatusDraft, Label: "Current edition", Evidence: []EvidenceAnchor{current},
	}}}); err != nil {
		t.Fatal(err)
	}
	workspace := &KnowledgeMapWorkspace{SessionToken: "pinned-source-test"}
	renders := 0
	render := func(_ context.Context, snapshot string, page int, _ ingest.ToolConfig) ([]byte, error) {
		renders++
		data, err := os.ReadFile(snapshot)
		if err != nil || !bytes.Equal(data, newBytes) || snapshot == source || page != current.Page {
			t.Fatalf("renderer did not receive verified current snapshot: page=%d err=%v", page, err)
		}
		return []byte("new edition image"), nil
	}
	for _, format := range []string{"text", "png", ""} {
		t.Run("format="+format, func(t *testing.T) {
			pin := func(anchor EvidenceAnchor) url.Values {
				return url.Values{"revision": {anchor.DocumentRevision}, "chunk_hash": {anchor.ChunkHash}, "evidence_hash": {anchor.EvidenceHash}}
			}
			cases := []struct {
				name   string
				values url.Values
				want   int
			}{
				{"current", pin(current), http.StatusOK},
				{"old", pin(old), http.StatusConflict},
				{"legacy ambiguous", url.Values{}, http.StatusConflict},
				{"incomplete", url.Values{"revision": {current.DocumentRevision}}, http.StatusBadRequest},
				{"unknown evidence", url.Values{"revision": {current.DocumentRevision}, "chunk_hash": {current.ChunkHash}, "evidence_hash": {ChunkContentHash("invented excerpt")}}, http.StatusNotFound},
				{"mixed editions", url.Values{"revision": {current.DocumentRevision}, "chunk_hash": {old.ChunkHash}, "evidence_hash": {current.EvidenceHash}}, http.StatusNotFound},
				{"duplicate pin", url.Values{"revision": {current.DocumentRevision, old.DocumentRevision}, "chunk_hash": {current.ChunkHash}, "evidence_hash": {current.EvidenceHash}}, http.StatusBadRequest},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					tc.values.Set("citation", current.CitationID)
					tc.values.Set("format", format)
					// Caller paths/pages never replace the coordinates selected by evidence.
					tc.values.Set("path", "untrusted.pdf")
					tc.values.Set("page", "999")
					r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/source?"+tc.values.Encode(), nil)
					r.AddCookie(knowledgeMapSourceCookie(workspace.SessionToken))
					w := httptest.NewRecorder()
					serveKnowledgeMapSourceWithRenderer(w, r, store, workspace, render)
					if w.Code != tc.want {
						t.Fatalf("status=%d want=%d body=%s", w.Code, tc.want, w.Body.String())
					}
					if w.Code != http.StatusOK {
						return
					}
					switch format {
					case "text":
						var result knowledgeMapSourceText
						if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Text != entry.Text || result.Anchor != current {
							t.Fatal("text response did not match selected evidence", result, err)
						}
					case "png":
						if w.Body.String() != "new edition image" {
							t.Fatal("wrong edition image")
						}
					default:
						if !bytes.Equal(w.Body.Bytes(), newBytes) {
							t.Fatal("wrong edition PDF")
						}
					}
				})
			}
		})
	}
	if renders != 1 {
		t.Fatal("stale or forged evidence reached renderer", renders)
	}
}
