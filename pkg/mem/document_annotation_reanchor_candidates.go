package mem

import (
	"context"
	"database/sql"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxDocumentAnnotationCandidates   = 50
	maxAnnotationCandidateEntries     = 1024
	maxAnnotationCandidateTextBytes   = 8 << 20
	maxAnnotationCandidateStoredBytes = 64 << 20
)

type DocumentAnnotationCandidateQuery struct {
	Revision int64 `json:"revision"`
	Limit    int   `json:"limit"`
}

// Complete describes this bounded, within-chunk search, not a full-document
// quotation matcher. Items are suggestions: they never change an annotation.
type DocumentAnnotationCandidates struct {
	AnnotationID       string                             `json:"annotation_id"`
	AnnotationRevision int64                              `json:"annotation_revision"`
	Quote              string                             `json:"quote"`
	Items              []DocumentAnnotationReanchorTarget `json:"items"`
	Limit              int                                `json:"limit"`
	Complete           bool                               `json:"complete"`
	ScannedEntries     int                                `json:"scanned_entries"`
	ScannedBytes       int64                              `json:"scanned_bytes"`
	TotalEntries       int64                              `json:"total_entries"`
	Warnings           []string                           `json:"warnings"`
	TruncatedReasons   []string                           `json:"truncated_reasons"`
}

func (out *DocumentAnnotationCandidates) incomplete(reason string) {
	for _, existing := range out.TruncatedReasons {
		if existing == reason {
			return
		}
	}
	out.Complete = false
	out.TruncatedReasons = append(out.TruncatedReasons, reason)
	messages := map[string]string{
		"binding_unresolved": "У перенесённой аннотации нет подтверждённой локальной привязки. Выберите документ и фрагмент вручную.",
		"entry_limit":        "Просмотрены не все фрагменты: предел поиска — 1024 фрагмента.",
		"text_budget":        "Поиск неполный: достигнут предел чтения текста 8 МиБ.",
		"chunk_limit":        "Пропущен слишком большой фрагмент: предел одного текста — 2 МиБ.",
		"metadata_budget":    "Поиск неполный: данные записей превышают предел чтения 64 МиБ.",
		"candidate_limit":    "Есть дополнительные совпадения сверх лимита результатов. Выберите место явно; единственность не установлена.",
		"invalid_entry":      "Пропущен фрагмент с некорректным текстом или неподтверждённой идентичностью источника.",
	}
	out.Warnings = append(out.Warnings, messages[reason])
}

// FindDocumentAnnotationReanchorCandidatesIn reads a single SQLite snapshot.
// Scope is the old logical document AND locator, never a basename, relocated
// physical path, neighbouring document, or external file. Even one exact match
// must be selected explicitly and revalidated by the existing preview/apply.
func FindDocumentAnnotationReanchorCandidatesIn(ctx context.Context, dir, id string, in DocumentAnnotationCandidateQuery) (DocumentAnnotationCandidates, error) {
	out := DocumentAnnotationCandidates{}
	if !validDocumentAnnotationID(id) || in.Revision < 1 || in.Limit < 0 || in.Limit > MaxDocumentAnnotationCandidates {
		return out, ErrDocumentAnnotationReanchorInvalid
	}
	if in.Limit == 0 {
		in.Limit = 20
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return out, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	old, err := readDocumentAnnotationTx(ctx, tx, id)
	if err != nil {
		return out, err
	}
	if old.Annotation.Revision != in.Revision {
		return out, ErrDocumentAnnotationConflict
	}
	out = DocumentAnnotationCandidates{AnnotationID: id, AnnotationRevision: in.Revision, Quote: old.Annotation.Quote,
		Items: []DocumentAnnotationReanchorTarget{}, Limit: in.Limit, Complete: true, TruncatedReasons: []string{},
		Warnings: []string{"Поиск точной цитаты выполняется внутри отдельных фрагментов того же документа. Совпадения на границе фрагментов не объединяются.",
			"Совпадение текста не подтверждает смысловую эквивалентность редакций. Выберите место, сравните привязки и подтвердите создание новой аннотации."}}
	if strings.HasPrefix(old.Anchor.SourcePath, documentAnnotationUnboundPrefix) {
		out.incomplete("binding_unresolved")
		return out, tx.Commit()
	}
	const scope = `document_id=? AND COALESCE(NULLIF(source_path,''),source_file,'')=?`
	args := []any{old.Anchor.DocumentID, old.Anchor.SourcePath}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM entries WHERE `+scope, args...).Scan(&out.TotalEntries); err != nil {
		return out, err
	}
	// Read only numeric sizes before allocating text, JSON metadata or vectors.
	rows, err := tx.QueryContext(ctx, `SELECT id,length(CAST(text AS BLOB)),`+annotationReaderEntrySizeSQL()+` FROM entries WHERE `+scope+` ORDER BY page,block_index,block_chunk_index,id LIMIT 1024`, args...)
	if err != nil {
		return out, err
	}
	type sizedEntry struct{ id, textBytes, storedBytes int64 }
	metadata := make([]sizedEntry, 0, min(int64(maxAnnotationCandidateEntries), out.TotalEntries))
	for rows.Next() {
		var row sizedEntry
		if err = rows.Scan(&row.id, &row.textBytes, &row.storedBytes); err != nil {
			rows.Close()
			return out, err
		}
		metadata = append(metadata, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if out.TotalEntries > int64(len(metadata)) {
		out.incomplete("entry_limit")
	}
	var storedBytes int64
	quoteRunes := utf8.RuneCountInString(out.Quote)
	for _, row := range metadata {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		if row.textBytes > maxDocumentAnnotationChunkBytes {
			out.incomplete("chunk_limit")
			continue
		}
		if row.storedBytes > maxAnnotationCandidateStoredBytes {
			out.incomplete("metadata_budget")
			continue
		}
		if out.ScannedBytes+row.textBytes > maxAnnotationCandidateTextBytes {
			out.incomplete("text_budget")
			break
		}
		if storedBytes+row.storedBytes > maxAnnotationCandidateStoredBytes {
			out.incomplete("metadata_budget")
			break
		}
		var text string
		if err = tx.QueryRowContext(ctx, `SELECT text FROM entries WHERE id=?`, row.id).Scan(&text); err != nil {
			return out, err
		}
		out.ScannedEntries++
		out.ScannedBytes += row.textBytes
		storedBytes += row.storedBytes
		if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			out.incomplete("invalid_entry")
			continue
		}
		first := strings.Index(text, out.Quote)
		if first < 0 {
			continue
		}
		entries, _, e := loadEntriesFromQuery(contextEntryReader{ctx, tx}, `SELECT `+storedEntryColumns+` FROM entries WHERE id=?`, row.id)
		if e != nil {
			return out, e
		}
		if len(entries) != 1 {
			return out, ErrDocumentAnnotationStale
		}
		entry := entries[0]
		anchor, e := EvidenceAnchorForEntry(entry, out.Quote)
		if e != nil {
			out.incomplete("invalid_entry")
			continue
		}
		pin, e := documentRestoreEntryDigest(entry)
		if e != nil {
			return out, e
		}
		runes := []rune(text)
		searchByte, searchRune := 0, 0
		for relative := first; relative >= 0; relative = strings.Index(text[searchByte:], out.Quote) {
			if err = ctx.Err(); err != nil {
				return out, err
			}
			at := searchByte + relative
			start := searchRune + utf8.RuneCountInString(text[searchByte:at])
			end := start + quoteRunes
			record := documentAnnotationRecord{Anchor: anchor, Annotation: DocumentAnnotation{Start: start, End: end}}
			state, e := resolveDocumentAnnotationStateTx(ctx, tx, record)
			if e != nil {
				return out, e
			}
			if state != EvidenceCurrent {
				out.incomplete("invalid_entry")
				break
			}
			if len(out.Items) == in.Limit {
				out.incomplete("candidate_limit")
				return out, tx.Commit()
			}
			out.Items = append(out.Items, DocumentAnnotationReanchorTarget{EntryID: entry.ID, Pin: pin,
				DocumentID: LibraryDocumentID(anchor.SourcePath, anchor.DocumentID), DocumentRevision: anchor.DocumentRevision,
				CitationID: anchor.CitationID, SourceName: truncateRunes(sourceName(entry), 256), Page: anchor.Page,
				BlockIndex: anchor.BlockIndex, BlockChunkIndex: anchor.BlockChunkIndex, Start: start, End: end, Quote: out.Quote,
				Prefix: string(runes[max(0, start-documentAnnotationContextRunes):start]), Suffix: string(runes[end:min(len(runes), end+documentAnnotationContextRunes)])})
			// Advance one codepoint rather than the quote length: overlaps are
			// separate choices, including repeated multi-byte Unicode quotations.
			_, width := utf8.DecodeRuneInString(text[at:])
			searchByte, searchRune = at+width, start+1
		}
	}
	return out, tx.Commit()
}
