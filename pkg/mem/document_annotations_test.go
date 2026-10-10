package mem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func documentAnnotationFixture(t *testing.T, text, quote string) (string, *Store, DocumentAnnotationCreate) {
	t.Helper()
	dir, s, _ := queryFixture(t)
	chunks := validStructuredChunks()
	for i := range chunks {
		chunks[i].Provenance.SourcePath = filepath.Join(dir, "private", "source.pdf")
		chunks[i].Provenance.DocumentRevision = ChunkContentHash(text)
	}
	chunks[0].Text = text
	chunks[0].Provenance.ChunkHash = ChunkContentHash(text)
	if err := s.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	entries := s.GetBySourceFile(chunks[0].Provenance.SourcePath)
	var entry Entry
	for _, e := range entries {
		if e.Text == text {
			entry = e
		}
	}
	pin, err := documentRestoreEntryDigest(entry)
	if err != nil {
		t.Fatal(err)
	}
	byteStart := strings.Index(text, quote)
	if byteStart < 0 {
		t.Fatal("quote absent")
	}
	start := utf8.RuneCountInString(text[:byteStart])
	return dir, s, DocumentAnnotationCreate{EntryID: entry.ID, Pin: pin, Start: start, End: start + utf8.RuneCountInString(quote), Quote: quote, Note: "Ручная заметка", Color: "yellow", RequestID: strings.Repeat("a", 32)}
}

func TestDocumentAnnotationLifecycleUnicodeHistoryReimportAndPrivacy(t *testing.T) {
	ctx := context.Background()
	quote := "давление 🌡️ 5 бар"
	text := "😀 Введение: " + quote + ". Завершение."
	dir, s, in := documentAnnotationFixture(t, text, quote)
	created, err := CreateDocumentAnnotationIn(ctx, dir, "alice", in)
	if err != nil {
		t.Fatal(err)
	}
	a := created.Annotation
	if created.Replayed || !validDocumentAnnotationID(a.ID) || a.Revision != 1 || a.Quote != quote || a.Prefix != "😀 Введение: " || a.Suffix != ". Завершение." || a.State != EvidenceCurrent || a.SourceName != "source.pdf" || a.Author != "alice" {
		t.Fatal(a)
	}
	read, err := ReadDocumentAnnotationIn(ctx, dir, a.ID)
	if err != nil || read != a {
		t.Fatal(read, err)
	}
	list, err := ListDocumentAnnotationsIn(ctx, dir, DocumentAnnotationQuery{DocumentID: a.DocumentID})
	if err != nil || list.Total != 1 || len(list.Items) != 1 || list.Limit != 50 {
		t.Fatal(list, err)
	}
	wrong, err := ListDocumentAnnotationsIn(ctx, dir, DocumentAnnotationQuery{DocumentID: strings.Repeat("0", 64)})
	if err != nil || wrong.Total != 0 {
		t.Fatal(wrong, err)
	}
	edit := DocumentAnnotationEdit{Revision: 1, Note: "Исправлено", Color: "blue", RequestID: strings.Repeat("b", 32)}
	saved, err := UpdateDocumentAnnotationIn(ctx, dir, "bob", a.ID, edit)
	if err != nil || saved.Annotation.Revision != 2 || saved.Annotation.Author != "alice" || saved.Annotation.Note != "Исправлено" || saved.Annotation.Quote != quote {
		t.Fatal(saved, err)
	}
	archive := true
	saved, err = UpdateDocumentAnnotationIn(ctx, dir, "alice", a.ID, DocumentAnnotationEdit{Revision: 2, Archived: &archive, RequestID: strings.Repeat("c", 32)})
	if err != nil || !saved.Annotation.Archived || saved.Annotation.Note != "Исправлено" {
		t.Fatal(saved, err)
	}
	list, err = ListDocumentAnnotationsIn(ctx, dir, DocumentAnnotationQuery{})
	if err != nil || list.Total != 0 {
		t.Fatal(list, err)
	}
	list, err = ListDocumentAnnotationsIn(ctx, dir, DocumentAnnotationQuery{Archived: true})
	if err != nil || list.Total != 1 {
		t.Fatal(list, err)
	}
	archive = false
	saved, err = UpdateDocumentAnnotationIn(ctx, dir, "alice", a.ID, DocumentAnnotationEdit{Revision: 3, Archived: &archive, RequestID: strings.Repeat("d", 32)})
	if err != nil || saved.Annotation.Archived {
		t.Fatal(saved, err)
	}
	history, err := ReadDocumentAnnotationHistoryIn(ctx, dir, a.ID, 0, 50)
	if err != nil || history.Total != 4 || len(history.Items) != 4 || history.Items[0].Action != "restore" || history.Items[1].Action != "archive" || history.Items[2].Actor != "bob" || history.Items[3].Annotation.Note != in.Note {
		t.Fatal(history, err)
	}
	for _, value := range []any{read, list, history, saved} {
		data, e := json.Marshal(value)
		if e != nil || strings.Contains(string(data), "source_path") || strings.Contains(string(data), "private") || strings.Contains(string(data), strings.ReplaceAll(dir, "\\", "\\\\")) {
			t.Fatalf("private locator leaked: %s %v", data, e)
		}
	}
	// Reimport replaces entries, but the saved quote/range/revision survive.
	chunks := validStructuredChunks()
	for i := range chunks {
		chunks[i].Provenance.SourcePath = filepath.Join(dir, "private", "source.pdf")
		chunks[i].Provenance.DocumentRevision = ChunkContentHash("updated source")
	}
	chunks[0].Text = "Теперь в другом месте повторена та же цитата: " + quote
	chunks[0].Provenance.ChunkHash = ChunkContentHash(chunks[0].Text)
	if err = s.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	read, err = ReadDocumentAnnotationIn(ctx, dir, a.ID)
	if err != nil || read.State != EvidenceStale || read.Quote != quote || read.DocumentRevision != a.DocumentRevision || read.Start != a.Start || read.End != a.End {
		t.Fatal(read, err)
	}
	if _, err = s.db.Exec(`DELETE FROM entries WHERE source_path=?`, chunks[0].Provenance.SourcePath); err != nil {
		t.Fatal(err)
	}
	read, err = ReadDocumentAnnotationIn(ctx, dir, a.ID)
	if err != nil || read.State != EvidenceMissing || read.Quote != quote {
		t.Fatal(read, err)
	}
	// Replaying a confirmed create returns its existing annotation even after
	// its old EntryID disappeared and subsequent user edits were saved.
	retry, err := CreateDocumentAnnotationIn(ctx, dir, "alice", in)
	if err != nil || !retry.Replayed || retry.Annotation.ID != a.ID || retry.Annotation.Revision != 1 || retry.Annotation.Note != in.Note || retry.Annotation.State != EvidenceMissing {
		t.Fatal(retry, err)
	}
	// Restoring the original pinned edition makes its old selection current
	// again without changing the annotation or appending an artificial edit.
	for i := range chunks {
		chunks[i].Provenance.DocumentRevision = ChunkContentHash(text)
	}
	chunks[0].Text = text
	chunks[0].Provenance.ChunkHash = ChunkContentHash(text)
	if err = s.ReplaceDocumentChunks(chunks[0].Provenance.SourcePath, chunks); err != nil {
		t.Fatal(err)
	}
	read, err = ReadDocumentAnnotationIn(ctx, dir, a.ID)
	if err != nil || read.State != EvidenceCurrent || read.Revision != 4 || read.Quote != quote || read.Start != a.Start {
		t.Fatal(read, err)
	}
	history, err = ReadDocumentAnnotationHistoryIn(ctx, dir, a.ID, 0, 50)
	if err != nil || history.Total != 4 {
		t.Fatal(history, err)
	}
}

func TestDocumentAnnotationExactOccurrenceAndPinnedConflicts(t *testing.T) {
	ctx := context.Background()
	dir, s, in := documentAnnotationFixture(t, "😀 один повтор, затем повтор.", "повтор")
	first, err := CreateDocumentAnnotationIn(ctx, dir, "alice", in)
	if err != nil {
		t.Fatal(err)
	}
	secondInput := in
	secondInput.Start = utf8.RuneCountInString("😀 один повтор, затем ")
	secondInput.End = secondInput.Start + utf8.RuneCountInString(in.Quote)
	secondInput.RequestID = strings.Repeat("b", 32)
	second, err := CreateDocumentAnnotationIn(ctx, dir, "alice", secondInput)
	if err != nil || second.Annotation.ID == first.Annotation.ID || second.Annotation.Start == first.Annotation.Start || second.Annotation.Prefix == first.Annotation.Prefix || second.Annotation.State != EvidenceCurrent {
		t.Fatal(second, err)
	}
	bad := in
	bad.Start++
	bad.End++
	bad.RequestID = strings.Repeat("c", 32)
	if _, err = CreateDocumentAnnotationIn(ctx, dir, "alice", bad); !errors.Is(err, ErrDocumentAnnotationInvalid) {
		t.Fatal("wrong occurrence", err)
	}
	bad = in
	bad.Note = "changed request"
	if _, err = CreateDocumentAnnotationIn(ctx, dir, "alice", bad); !errors.Is(err, ErrDocumentAnnotationRequest) {
		t.Fatal("reused receipt", err)
	}
	edit := DocumentAnnotationEdit{Revision: 1, Note: "new", Color: "pink", RequestID: strings.Repeat("d", 32)}
	updated, err := UpdateDocumentAnnotationIn(ctx, dir, "alice", first.Annotation.ID, edit)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := UpdateDocumentAnnotationIn(ctx, dir, "alice", first.Annotation.ID, edit)
	if err != nil || !retry.Replayed || retry.Annotation.Revision != 2 {
		t.Fatal(retry, err)
	}
	conflict := edit
	conflict.RequestID = strings.Repeat("e", 32)
	if _, err = UpdateDocumentAnnotationIn(ctx, dir, "alice", first.Annotation.ID, conflict); !errors.Is(err, ErrDocumentAnnotationConflict) {
		t.Fatal(err)
	}
	edit.Note = ""
	edit.Revision = updated.Annotation.Revision
	edit.RequestID = strings.Repeat("f", 32)
	cleared, err := UpdateDocumentAnnotationIn(ctx, dir, "alice", first.Annotation.ID, edit)
	if err != nil || cleared.Annotation.Note != "" {
		t.Fatal(cleared, err)
	}
	priorEdit := DocumentAnnotationEdit{Revision: 1, Note: "new", Color: "pink", RequestID: strings.Repeat("d", 32)}
	late, err := UpdateDocumentAnnotationIn(ctx, dir, "alice", first.Annotation.ID, priorEdit)
	if err != nil || !late.Replayed || late.Annotation.Revision != 2 || late.Annotation.Note != "new" {
		t.Fatal("late receipt lost exact result", late, err)
	}
	if _, err = s.db.Exec(`UPDATE entries SET text='changed' WHERE id=?`, in.EntryID); err != nil {
		t.Fatal(err)
	}
	bad = in
	bad.RequestID = strings.Repeat("1", 32)
	if _, err = CreateDocumentAnnotationIn(ctx, dir, "alice", bad); !errors.Is(err, ErrDocumentAnnotationStale) {
		t.Fatal("changed source pin", err)
	}
	// The database enforces immutable source identity and append-only history.
	if _, err = s.db.Exec(`UPDATE document_annotations SET start_offset=start_offset+1 WHERE id=?`, first.Annotation.ID); err == nil {
		t.Fatal("source range mutated")
	}
	if _, err = s.db.Exec(`UPDATE document_annotation_history SET action='fake' WHERE annotation_id=?`, first.Annotation.ID); err == nil {
		t.Fatal("history changed")
	}
	if _, err = s.db.Exec(`DELETE FROM document_annotation_receipts WHERE request_id=?`, in.RequestID); err == nil {
		t.Fatal("receipt deleted")
	}
}

func TestDocumentAnnotationValidationReadCompatibilityAndCancellation(t *testing.T) {
	ctx := context.Background()
	dir, s, in := documentAnnotationFixture(t, "Текст цитаты.", "цитаты")
	page, err := ListDocumentAnnotationsIn(ctx, dir, DocumentAnnotationQuery{})
	if err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatal(page, err)
	}
	missing := "ann-" + strings.Repeat("0", 32)
	if _, err = ReadDocumentAnnotationIn(ctx, dir, missing); !errors.Is(err, ErrDocumentAnnotationNotFound) {
		t.Fatal(err)
	}
	if _, err = ReadDocumentAnnotationHistoryIn(ctx, dir, missing, 0, 0); !errors.Is(err, ErrDocumentAnnotationNotFound) {
		t.Fatal(err)
	}
	badInputs := []DocumentAnnotationCreate{}
	for _, change := range []func(*DocumentAnnotationCreate){
		func(x *DocumentAnnotationCreate) { x.Quote = string([]byte{0xff}) }, func(x *DocumentAnnotationCreate) { x.Note = string([]byte{0xed, 0xa0, 0x80}) }, func(x *DocumentAnnotationCreate) { x.Note = "nul\x00" },
		func(x *DocumentAnnotationCreate) { x.Start = -1 }, func(x *DocumentAnnotationCreate) { x.End = x.Start }, func(x *DocumentAnnotationCreate) { x.Quote = "not in text"; x.End = x.Start + len(x.Quote) },
		func(x *DocumentAnnotationCreate) { x.Color = "red" }, func(x *DocumentAnnotationCreate) { x.Note = strings.Repeat("я", 8001) }, func(x *DocumentAnnotationCreate) { x.Pin = "invalid" }, func(x *DocumentAnnotationCreate) { x.RequestID = "invalid" },
	} {
		bad := in
		change(&bad)
		badInputs = append(badInputs, bad)
	}
	for i, bad := range badInputs {
		if _, err = CreateDocumentAnnotationIn(ctx, dir, "alice", bad); !errors.Is(err, ErrDocumentAnnotationInvalid) {
			t.Fatalf("input %d: %v", i, err)
		}
	}
	var tables int
	s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'document_annotation%'`).Scan(&tables)
	if tables != 0 {
		t.Fatal("read or failed creation left schema", tables)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = CreateDocumentAnnotationIn(cancelled, dir, "alice", in); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled create", err)
	}
	if _, err = ListDocumentAnnotationsIn(cancelled, dir, DocumentAnnotationQuery{}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled read", err)
	}
	created, err := CreateDocumentAnnotationIn(ctx, dir, "alice", in)
	if err != nil {
		t.Fatal(err)
	}
	flag := true
	if _, err = UpdateDocumentAnnotationIn(ctx, dir, "alice", created.Annotation.ID, DocumentAnnotationEdit{Revision: 1, Archived: &flag, Note: "accidental overwrite", RequestID: strings.Repeat("2", 32)}); !errors.Is(err, ErrDocumentAnnotationInvalid) {
		t.Fatal(err)
	}
	for _, q := range []DocumentAnnotationQuery{{Offset: -1}, {Offset: 1000001}, {Limit: 51}, {DocumentID: "\x00"}} {
		if _, err = ListDocumentAnnotationsIn(ctx, dir, q); !errors.Is(err, ErrDocumentAnnotationInvalid) {
			t.Fatal(q, err)
		}
	}
}

func TestDocumentAnnotationQuotasRollbackAndReceiptReplay(t *testing.T) {
	for _, kind := range []string{"records", "record-bytes", "history", "receipt-count", "receipt-bytes", "chunk"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			dir, s, in := documentAnnotationFixture(t, "Точный фрагмент.", "фрагмент")
			created, err := CreateDocumentAnnotationIn(ctx, dir, "alice", in)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "records":
				_, err = s.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<9999)
INSERT INTO document_annotations SELECT 'ann-'||printf('%032x',x),revision,document_id,anchor_json,start_offset,end_offset,prefix,suffix,note,color,1,author,created,updated FROM document_annotations,n WHERE id=?`, created.Annotation.ID)
			case "record-bytes":
				_, err = s.db.Exec(`INSERT INTO document_annotations SELECT 'ann-'||printf('%032x',1),revision,document_id,anchor_json,start_offset,end_offset,prefix,suffix,CAST(zeroblob(?) AS TEXT),color,1,author,created,updated FROM document_annotations WHERE id=?`, maxDocumentAnnotationBytes, created.Annotation.ID)
			case "history":
				_, err = s.db.Exec(`INSERT INTO document_annotation_history(annotation_id,revision,action,actor,created,payload,payload_hash) VALUES('quota',1,'quota','test','now',CAST(zeroblob(?) AS TEXT),'x')`, maxDocumentAnnotationHistoryBytes)
			case "receipt-count":
				_, err = s.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<99999) INSERT INTO document_annotation_receipts SELECT printf('%032x',x),'x','x',1,'x' FROM n`)
			case "receipt-bytes":
				_, err = s.db.Exec(`INSERT INTO document_annotation_receipts VALUES('quota',CAST(zeroblob(?) AS TEXT),'x',1,'x')`, maxDocumentAnnotationReceiptBytes)
			case "chunk":
				_, err = s.db.Exec(`UPDATE entries SET text=? WHERE id=?`, strings.Repeat("x", maxDocumentAnnotationChunkBytes+1), in.EntryID)
			}
			if err != nil {
				t.Fatal(err)
			}
			// None of the aggregate quotas can turn a previously confirmed retry
			// into a second mutation or demand the former source pin again.
			retry, err := CreateDocumentAnnotationIn(ctx, dir, "alice", in)
			if err != nil || !retry.Replayed || retry.Annotation.ID != created.Annotation.ID {
				t.Fatal(retry, err)
			}
			var beforeRows, beforeHistory, beforeReceipts int
			s.db.QueryRow(`SELECT COUNT(*) FROM document_annotations`).Scan(&beforeRows)
			s.db.QueryRow(`SELECT COUNT(*) FROM document_annotation_history`).Scan(&beforeHistory)
			s.db.QueryRow(`SELECT COUNT(*) FROM document_annotation_receipts`).Scan(&beforeReceipts)
			other := in
			other.RequestID = strings.Repeat("b", 32)
			if _, err = CreateDocumentAnnotationIn(ctx, dir, "alice", other); !errors.Is(err, ErrDocumentAnnotationLimit) {
				t.Fatal("quota accepted create", err)
			}
			if kind != "chunk" && kind != "records" {
				_, err = UpdateDocumentAnnotationIn(ctx, dir, "alice", created.Annotation.ID, DocumentAnnotationEdit{Revision: 1, Note: "must rollback", Color: "green", RequestID: strings.Repeat("c", 32)})
				if !errors.Is(err, ErrDocumentAnnotationLimit) {
					t.Fatal("quota accepted edit", err)
				}
			}
			for _, check := range []struct {
				table string
				want  int
			}{{"document_annotations", beforeRows}, {"document_annotation_history", beforeHistory}, {"document_annotation_receipts", beforeReceipts}} {
				var n int
				if err = s.db.QueryRow(`SELECT COUNT(*) FROM ` + check.table).Scan(&n); err != nil || n != check.want {
					t.Fatal(check.table, n, check.want, err)
				}
			}
			read, err := ReadDocumentAnnotationIn(ctx, dir, created.Annotation.ID)
			if err != nil || read.Revision != 1 || read.Note != in.Note {
				t.Fatal(read, err)
			}
		})
	}
}

func TestDocumentAnnotationPaginationAndBoundedPrefix(t *testing.T) {
	text := strings.Repeat("😀", 80) + "цитата" + strings.Repeat("я", 80)
	dir, _, in := documentAnnotationFixture(t, text, "цитата")
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		in.RequestID = fmt.Sprintf("%032x", i+1)
		created, err := CreateDocumentAnnotationIn(ctx, dir, "alice", in)
		if err != nil || utf8.RuneCountInString(created.Annotation.Prefix) != 48 || utf8.RuneCountInString(created.Annotation.Suffix) != 48 {
			t.Fatal(created, err)
		}
	}
	page, err := ListDocumentAnnotationsIn(ctx, dir, DocumentAnnotationQuery{Offset: 1, Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Total != 3 || page.Offset != 1 || page.Limit != 1 {
		t.Fatal(page, err)
	}
}
