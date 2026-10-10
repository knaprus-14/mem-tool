package webjobs

import (
	"archive/zip"
	"bytes"
	"context"
	"testing"

	"github.com/knaprus-14/mem-tool/internal/workspace"
)

func TestAdditionalDocumentFormatsThroughQueueAndImporter(t *testing.T) {
	for _, ext := range []string{"txt", "csv", "tsv", "json", "xml", "docx"} {
		t.Run(ext, func(t *testing.T) {
			_, w, q, service := fixture(t)
			model(t, w)
			data := []byte("Содержимое документа 5 бар.")
			if ext == "docx" {
				var b bytes.Buffer
				z := zip.NewWriter(&b)
				part, err := z.Create("word/document.xml")
				if err != nil {
					t.Fatal(err)
				}
				part.Write([]byte(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Содержимое документа 5 бар.</w:t></w:r></w:p></w:body></w:document>`))
				z.Close()
				data = b.Bytes()
			}
			actor := workspace.Actor{ID: "alice"}
			job, _, err := q.Enqueue(context.Background(), actor, "Источник."+ext, bytes.NewReader(data), Metadata{})
			if err != nil {
				t.Fatal(err)
			}
			if worked, err := service.ProcessNext(context.Background()); err != nil || !worked {
				t.Fatal(worked, err)
			}
			result, err := q.Get(context.Background(), actor, job.ID)
			if err != nil || result.Status != "succeeded" || result.Chunks == 0 {
				t.Fatal(result, err)
			}
		})
	}
}
