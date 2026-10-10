package mem

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

func annotationKnowledgeFixture(t *testing.T) (string, *Store, DocumentAnnotation, DocumentAnnotationKnowledgeCreate) {
	t.Helper()
	dir, s, in := documentAnnotationFixture(t, "😀 Начало: точная цитата. Конец.", "точная цитата")
	created, err := CreateDocumentAnnotationIn(context.Background(), dir, "alice", in)
	if err != nil {
		t.Fatal(err)
	}
	return dir, s, created.Annotation, DocumentAnnotationKnowledgeCreate{Revision: 1, Kind: KnowledgeNodeNote, Label: "Моя мысль", Body: "Явно введённый текст", RequestID: strings.Repeat("b", 32)}
}

func TestAnnotationKnowledgeLifecycleExactReplayAndPrivacy(t *testing.T) {
	ctx := context.Background()
	dir, s, a, in := annotationKnowledgeFixture(t)
	page, err := ListDocumentAnnotationKnowledgeLinksIn(ctx, dir, a.ID, 0, 0)
	if err != nil || page.Total != 0 || page.Limit != 50 {
		t.Fatal(page, err)
	}
	var schemaCount int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='document_annotation_knowledge_links'`).Scan(&schemaCount); err != nil || schemaCount != 0 {
		t.Fatal("GET mutated schema", schemaCount, err)
	}
	created, err := CreateDocumentAnnotationKnowledgeNodeIn(ctx, dir, "alice", a.ID, in)
	if err != nil || created.Replayed || created.Node.Status != KnowledgeStatusDraft || created.Node.Origin != KnowledgeOriginManual || created.Node.Body != in.Body || created.Link.AnnotationRevision != 1 {
		t.Fatal(created, err)
	}
	page, err = ListDocumentAnnotationKnowledgeLinksIn(ctx, dir, a.ID, 0, 1)
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].NodeChanged || !page.Items[0].EvidenceLinked {
		t.Fatal(page, err)
	}
	if _, err = UpdateDocumentAnnotationIn(ctx, dir, "alice", a.ID, DocumentAnnotationEdit{Revision: 1, Note: "Новый комментарий", Color: "pink", RequestID: strings.Repeat("c", 32)}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE knowledge_nodes SET body='Изменённый узел' WHERE id=?`, created.Node.ID); err != nil {
		t.Fatal(err)
	}
	retry, err := CreateDocumentAnnotationKnowledgeNodeIn(ctx, dir, "alice", a.ID, in)
	if err != nil || !retry.Replayed || retry.Node != created.Node || retry.Link != created.Link {
		t.Fatal("retry did not preserve creation revision", retry, err)
	}
	page, err = ListKnowledgeNodeDocumentAnnotationsIn(ctx, dir, created.Node.ID, 0, 50)
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	item := page.Items[0]
	if item.Annotation.Revision != 2 || item.Snapshot.Revision != 1 || item.Snapshot.Note != a.Note || item.Node.Body != "Изменённый узел" || item.Creation.Node.Body != in.Body || !item.NodeChanged || !item.EvidenceLinked {
		t.Fatal(item)
	}
	for _, value := range []any{created, retry, page} {
		data, _ := json.Marshal(value)
		if strings.Contains(string(data), "source_path") || strings.Contains(string(data), "private") {
			t.Fatalf("private path leaked: %s", data)
		}
	}
	for _, statement := range []string{`UPDATE document_annotation_knowledge_links SET author='bob'`, `DELETE FROM document_annotation_knowledge_links`, `UPDATE document_annotation_knowledge_receipts SET request_hash='x'`, `DELETE FROM document_annotation_knowledge_receipts`} {
		if _, err = s.db.Exec(statement); err == nil {
			t.Fatal("immutable record changed", statement)
		}
	}
	if _, err = s.db.Exec(`DELETE FROM knowledge_node_evidence WHERE node_id=?`, created.Node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`DELETE FROM knowledge_nodes WHERE id=?`, created.Node.ID); err != nil {
		t.Fatal(err)
	}
	page, err = ListKnowledgeNodeDocumentAnnotationsIn(ctx, dir, created.Node.ID, 0, 50)
	if err != nil || len(page.Items) != 1 || !page.Items[0].MissingNode || !page.Items[0].NodeChanged || page.Items[0].EvidenceLinked {
		t.Fatal("orphan provenance lost", page, err)
	}
	retry, err = CreateDocumentAnnotationKnowledgeNodeIn(ctx, dir, "alice", a.ID, in)
	if err != nil || !retry.Replayed || retry.Node != created.Node {
		t.Fatal("deleted node recreated by retry", retry, err)
	}
}

func TestAnnotationKnowledgeCoordinatesArePartOfExactEvidence(t *testing.T) {
	for _, coordinate := range []string{"page", "block_index", "block_chunk_index"} {
		t.Run(coordinate, func(t *testing.T) {
			dir, s, a, in := annotationKnowledgeFixture(t)
			created, err := CreateDocumentAnnotationKnowledgeNodeIn(context.Background(), dir, "alice", a.ID, in)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec(`UPDATE knowledge_node_evidence SET `+coordinate+`=`+coordinate+`+1 WHERE node_id=?`, created.Node.ID); err != nil {
				t.Fatal(err)
			}
			page, err := ListDocumentAnnotationKnowledgeLinksIn(context.Background(), dir, a.ID, 0, 50)
			if err != nil || len(page.Items) != 1 || !page.Items[0].NodeChanged || page.Items[0].EvidenceLinked {
				t.Fatal(page, err)
			}
		})
	}
}

func TestAnnotationKnowledgeValidationStaleAndAtomicRollback(t *testing.T) {
	ctx := context.Background()
	dir, s, a, in := annotationKnowledgeFixture(t)
	bad := in
	bad.Kind = KnowledgeNodeKind("claim")
	if _, err := CreateDocumentAnnotationKnowledgeNodeIn(ctx, dir, "alice", a.ID, bad); !errors.Is(err, ErrDocumentAnnotationKnowledgeInvalid) {
		t.Fatal(err)
	}
	bad = in
	bad.Revision = 2
	if _, err := CreateDocumentAnnotationKnowledgeNodeIn(ctx, dir, "alice", a.ID, bad); !errors.Is(err, ErrDocumentAnnotationConflict) {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(annotationKnowledgeSchema + `CREATE TRIGGER fail_annotation_knowledge BEFORE INSERT ON document_annotation_knowledge_links BEGIN SELECT RAISE(ABORT,'test rollback'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateDocumentAnnotationKnowledgeNodeIn(ctx, dir, "alice", a.ID, in); err == nil {
		t.Fatal("forced failure succeeded")
	}
	for _, table := range []string{"knowledge_nodes", "knowledge_node_evidence", "document_annotation_knowledge_links", "document_annotation_knowledge_receipts"} {
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("non-atomic create", table, count, err)
		}
	}
	if _, err := s.db.Exec(`DROP TRIGGER fail_annotation_knowledge`); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateDocumentAnnotationKnowledgeNodeIn(ctx, dir, "alice", a.ID, in); err != nil {
		t.Fatal(err)
	}
	bad = in
	bad.Body += " changed"
	if _, err := CreateDocumentAnnotationKnowledgeNodeIn(ctx, dir, "alice", a.ID, bad); !errors.Is(err, ErrDocumentAnnotationRequest) {
		t.Fatal(err)
	}
	archive := true
	if _, err := UpdateDocumentAnnotationIn(ctx, dir, "alice", a.ID, DocumentAnnotationEdit{Revision: 1, Archived: &archive, RequestID: strings.Repeat("d", 32)}); err != nil {
		t.Fatal(err)
	}
	bad = in
	bad.Revision = 2
	bad.RequestID = strings.Repeat("e", 32)
	if _, err := CreateDocumentAnnotationKnowledgeNodeIn(ctx, dir, "alice", a.ID, bad); !errors.Is(err, ErrDocumentAnnotationStale) {
		t.Fatal(err)
	}
	archive = false
	if _, err := UpdateDocumentAnnotationIn(ctx, dir, "alice", a.ID, DocumentAnnotationEdit{Revision: 2, Archived: &archive, RequestID: strings.Repeat("f", 32)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM entries`); err != nil {
		t.Fatal(err)
	}
	bad.Revision = 3
	if _, err := CreateDocumentAnnotationKnowledgeNodeIn(ctx, dir, "alice", a.ID, bad); !errors.Is(err, ErrDocumentAnnotationStale) {
		t.Fatal(err)
	}
}

func TestAnnotationKnowledgeQuotaAndReadPreflight(t *testing.T) {
	ctx := context.Background()
	dir, s, a, in := annotationKnowledgeFixture(t)
	if _, err := s.db.Exec(annotationKnowledgeSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<1000) INSERT INTO document_annotation_knowledge_links SELECT 'quota-'||i,?,1,'alice','date','{}','hash' FROM n`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateDocumentAnnotationKnowledgeNodeIn(ctx, dir, "alice", a.ID, in); !errors.Is(err, ErrDocumentAnnotationKnowledgeLimit) {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM knowledge_nodes`).Scan(&count); err != nil || count != 0 {
		t.Fatal("quota left graph node", count, err)
	}
	if _, err := s.db.Exec(`INSERT INTO document_annotation_knowledge_links VALUES('oversized',?,1,'alice','zzzz',?,'hash')`, a.ID, strings.Repeat("x", maxAnnotationKnowledgeRowBytes+1)); err != nil {
		t.Fatal(err)
	}
	if _, err := ListDocumentAnnotationKnowledgeLinksIn(ctx, dir, a.ID, 0, 1); !errors.Is(err, ErrDocumentAnnotationKnowledgeLimit) {
		t.Fatal("oversized payload was read", err)
	}
}

func TestAnnotationKnowledgeConcurrentReceipt(t *testing.T) {
	dir, s, a, in := annotationKnowledgeFixture(t)
	var wg sync.WaitGroup
	results := make([]DocumentAnnotationKnowledgeResult, 5)
	errs := make([]error, len(results))
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = CreateDocumentAnnotationKnowledgeNodeIn(context.Background(), dir, "alice", a.ID, in)
		}(i)
	}
	wg.Wait()
	for i, result := range results {
		if errs[i] != nil || result.Node.ID != results[0].Node.ID {
			t.Fatal(i, result, errs[i])
		}
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM knowledge_nodes`).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate graph node", count, err)
	}
}
