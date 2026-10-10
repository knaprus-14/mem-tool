package mem

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mapDocumentFixture(t *testing.T) (string, *Store, string, string, string, string) {
	t.Helper()
	return mapDocumentFixtureAtPage(t, 4)
}

func mapDocumentFixtureAtPage(t *testing.T, pageNumber int) (string, *Store, string, string, string, string) {
	t.Helper()
	dir, s, id, root, in := webAIFixture(t)
	ctx := context.Background()
	path := filepath.Join(filepath.Dir(dir), "source.pdf")
	content := []byte("%PDF-1.4\nverified fixture\n%%EOF")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	chunks := validStructuredChunks()
	for i := range chunks {
		chunks[i].Provenance.SourcePath = path
		chunks[i].Provenance.Page = pageNumber
	}
	if err := s.ReplaceDocumentChunks(path, chunks); err != nil {
		t.Fatal(err)
	}
	page, err := ListWebMapCandidatesIn(ctx, dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	var selected QueryHit
	for _, hit := range page.Items {
		var source string
		s.db.QueryRow(`SELECT source_path FROM entries WHERE id=?`, hit.ID).Scan(&source)
		if source == path {
			selected = hit
			break
		}
	}
	if selected.ID == 0 {
		t.Fatal("missing fixture evidence")
	}
	result, err := ActWebMapSourceIn(ctx, dir, "alice", id, root, "add", WebMapSourceInput{Revision: in.Revision, RequestID: strings.Repeat("f", 32), EntryID: selected.ID, Pin: selected.Pin})
	if err != nil || result.Revision != 2 {
		t.Fatal(result, err)
	}
	sources, err := ListWebMapSourcesIn(ctx, dir, id, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(documentSourceFilesSchema); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	if _, err = s.db.Exec(`INSERT INTO document_source_files SELECT DISTINCT document_id,document_revision,source_path,? FROM entries WHERE source_path=?`, fmt.Sprintf("sha256:%x", sum), path); err != nil {
		t.Fatal(err)
	}
	return dir, s, id, root, sources.Items[0].ID, path
}

func TestWebMapDocumentWithoutPageOrFingerprint(t *testing.T) {
	dir, s, id, node, source, _ := mapDocumentFixtureAtPage(t, 0)
	if _, err := s.db.Exec(`DELETE FROM document_source_files`); err != nil {
		t.Fatal(err)
	}
	view, err := ReadWebMapDocumentIn(context.Background(), dir, id, node, source, 2)
	if err != nil || view.Page != 0 || view.State != "current" || view.OriginalPDF || len(view.Passages) != 2 || !strings.Contains(view.Notice, "нет номера страницы") {
		t.Fatal(view, err)
	}
	if _, close, err := OpenWebMapDocumentPDFIn(context.Background(), dir, id, node, source, 2, true); err == nil {
		close()
		t.Fatal("unverified legacy file served")
	}
}
func TestWebMapDocumentPageAndVerifiedPDF(t *testing.T) {
	dir, _, id, node, source, _ := mapDocumentFixture(t)
	ctx := context.Background()
	view, e := ReadWebMapDocumentIn(ctx, dir, id, node, source, 2)
	if e != nil {
		t.Fatal(e)
	}
	if view.State != "current" || view.Title != "source.pdf" || !view.OriginalPDF || len(view.Passages) == 0 {
		t.Fatal(view)
	}
	matches := 0
	for _, p := range view.Passages {
		if p.Match {
			matches++
		}
	}
	if matches != 1 {
		t.Fatal("citation not highlighted", view)
	}
	for _, local := range []bool{false, true} {
		file, close, e := OpenWebMapDocumentPDFIn(ctx, dir, id, node, source, 2, local)
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(file)
		close()
		if e != nil || !strings.HasPrefix(string(b), "%PDF-") {
			t.Fatal(e, string(b))
		}
	}
	if _, e = ReadWebMapDocumentIn(ctx, dir, id, node, source, 1); e != ErrClassicMindMapRevisionConflict {
		t.Fatal("stale map revision", e)
	}
	if _, e = ReadWebMapDocumentIn(ctx, dir, id, "other-node", source, 2); e == nil {
		t.Fatal("wrong node source accepted")
	}
}
func TestWebMapDocumentChangedFileAndStaleEvidence(t *testing.T) {
	dir, s, id, node, source, path := mapDocumentFixture(t)
	ctx := context.Background()
	if e := os.WriteFile(path, []byte("%PDF-1.4 replaced"), 0600); e != nil {
		t.Fatal(e)
	}
	if file, close, e := OpenWebMapDocumentPDFIn(ctx, dir, id, node, source, 2, true); e == nil {
		close()
		t.Fatal("changed file served", file)
	}
	if _, e := s.db.Exec(`DELETE FROM entries WHERE source_path=?`, path); e != nil {
		t.Fatal(e)
	}
	view, e := ReadWebMapDocumentIn(ctx, dir, id, node, source, 2)
	if e != nil || view.State == "current" || view.OriginalPDF || len(view.Passages) != 0 || view.Excerpt == "" {
		t.Fatal(view, e)
	}
}
func TestWebMapDocumentHostedEscapeRejected(t *testing.T) {
	dir, s, id, node, source, path := mapDocumentFixture(t)
	ctx := context.Background()
	f, err := os.CreateTemp("", "mem-outside-*.pdf")
	if err != nil {
		t.Fatal(err)
	}
	outside := f.Name()
	f.Close()
	t.Cleanup(func() { os.Remove(outside) })
	data, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(outside, data, 0600); e != nil {
		t.Fatal(e)
	}
	// Rewrite all pinned paths consistently: hosted filesystem containment must
	// still reject the source, independently of its matching fingerprint.
	for _, table := range []string{"entries", "mind_map_node_sources", "document_source_files"} {
		if _, e = s.db.Exec(`UPDATE `+table+` SET source_path=? WHERE source_path=?`, outside, path); e != nil {
			t.Fatal(e)
		}
	}
	if file, close, e := OpenWebMapDocumentPDFIn(ctx, dir, id, node, source, 2, false); e == nil {
		close()
		t.Fatal("outside workspace served", file)
	}
}
