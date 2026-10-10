package ingest

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/knaprus-14/mem-tool/internal/sourcefile"
)

// TextExtractor treats markup and table delimiters as source text. In
// particular, page-marker-like strings do not invent physical page provenance.
type TextExtractor struct{}

func (TextExtractor) Extract(ctx context.Context, path string) (Document, error) {
	data, err := sourcefile.ReadText(ctx, path)
	if err != nil {
		return Document{}, err
	}
	return plainDocument(path, string(data), "text", "text/plain")
}

func plainDocument(path, text string, format Format, mediaType string) (Document, error) {
	path, err := canonicalSourcePath(path)
	if err != nil {
		return Document{}, err
	}
	text = strings.TrimPrefix(text, "\ufeff")
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	if strings.TrimSpace(text) == "" {
		return Document{}, fmt.Errorf("в документе нет извлечённого текста: %s", path)
	}
	doc := Document{ID: documentID(path), SourcePath: path, Format: format, MediaType: mediaType, Title: filepath.Base(path), Markdown: text}
	for _, part := range strings.Split(text, "\n\n") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		doc.Blocks = append(doc.Blocks, Block{Index: len(doc.Blocks), Text: part, Extraction: "text", OCRConfidence: -1})
	}
	doc.Revision = ContentRevision(doc)
	return doc, nil
}

// DOCX extraction needs no Office process. Only known text parts are decoded;
// nothing is executed or fetched, and expanded XML has an aggregate size bound.
type DOCXExtractor struct{}

func (DOCXExtractor) Extract(ctx context.Context, path string) (Document, error) {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return Document{}, fmt.Errorf("не удалось открыть DOCX: %w", err)
	}
	defer archive.Close()
	if len(archive.File) > 20000 {
		return Document{}, fmt.Errorf("DOCX содержит слишком много частей")
	}
	var main *zip.File
	extras := []*zip.File{}
	hasImages := false
	var size uint64
	for _, f := range archive.File {
		name := f.Name
		if strings.HasPrefix(name, "word/media/") {
			hasImages = true
		}
		selected := name == "word/document.xml" || name == "word/footnotes.xml" || name == "word/endnotes.xml" || strings.HasPrefix(name, "word/header") && strings.HasSuffix(name, ".xml") || strings.HasPrefix(name, "word/footer") && strings.HasSuffix(name, ".xml")
		if !selected {
			continue
		}
		if f.UncompressedSize64 > sourcefile.MaxTextBytes || size > sourcefile.MaxTextBytes-f.UncompressedSize64 {
			return Document{}, fmt.Errorf("распакованный текст DOCX превышает 64 МиБ")
		}
		size += f.UncompressedSize64
		if name == "word/document.xml" {
			if main != nil {
				return Document{}, fmt.Errorf("DOCX содержит повторную основную часть")
			}
			main = f
		} else {
			extras = append(extras, f)
		}
	}
	if main == nil {
		return Document{}, fmt.Errorf("в DOCX отсутствует word/document.xml")
	}
	sort.Slice(extras, func(i, j int) bool { return extras[i].Name < extras[j].Name })
	parts := append([]*zip.File{main}, extras...)
	text := strings.Builder{}
	for _, part := range parts {
		if err = ctx.Err(); err != nil {
			return Document{}, err
		}
		reader, e := part.Open()
		if e != nil {
			return Document{}, e
		}
		value, e := docxText(ctx, io.LimitReader(reader, sourcefile.MaxTextBytes+1))
		reader.Close()
		if e != nil {
			return Document{}, fmt.Errorf("текст DOCX %s: %w", part.Name, e)
		}
		text.WriteString(value)
		text.WriteString("\n\n")
	}
	doc, err := plainDocument(path, text.String(), "docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	if err != nil {
		return doc, err
	}
	doc.Warnings = append(doc.Warnings, "DOCX: извлечён текст абзацев, таблиц, сносок и колонтитулов; физические страницы не определяются. Формулы Office и диаграммы могут требовать экспорта в PDF.")
	if hasImages {
		doc.Warnings = append(doc.Warnings, "DOCX содержит изображения: текст внутри изображений не распознавался. Для OCR экспортируйте документ в PDF.")
	}
	return doc, nil
}

func docxText(ctx context.Context, reader io.Reader) (string, error) {
	const word = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	const strictWord = "http://purl.oclc.org/ooxml/wordprocessingml/main"
	dec := xml.NewDecoder(reader)
	text := strings.Builder{}
	inText := false
	deleted := 0
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		token, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Space != word && t.Name.Space != strictWord {
				continue
			}
			if t.Name.Local == "del" {
				deleted++
			}
			if deleted > 0 {
				continue
			}
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				text.WriteByte('\t')
			case "br", "cr":
				text.WriteByte('\n')
			}
		case xml.EndElement:
			if t.Name.Space != word && t.Name.Space != strictWord {
				continue
			}
			if t.Name.Local == "del" {
				deleted--
				continue
			}
			if deleted > 0 {
				continue
			}
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				text.WriteByte('\n')
			case "tc":
				text.WriteByte('\t')
			case "tr":
				text.WriteByte('\n')
			}
		case xml.CharData:
			if inText && deleted == 0 {
				text.Write(t)
			}
		}
		if text.Len() > sourcefile.MaxTextBytes {
			return "", fmt.Errorf("текст DOCX превышает 64 МиБ")
		}
	}
	return text.String(), nil
}
