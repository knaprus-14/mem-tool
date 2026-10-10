package mem

import (
	"context"
	"net/http"
	"os"
	"path/filepath"

	"github.com/knaprus-14/mem-tool/internal/applog"
	"github.com/knaprus-14/mem-tool/pkg/ingest"
)

// Limit native renderers across all open maps, including the shared web app.
var knowledgeSourcePreviewSlots = make(chan struct{}, 2)

type knowledgePageRenderer func(context.Context, string, int, ingest.ToolConfig) ([]byte, error)

func serveKnowledgeMapPagePNG(w http.ResponseWriter, r *http.Request, store *Store, anchor EvidenceAnchor, snapshotPath string, render knowledgePageRenderer) {
	dir := filepath.Dir(store.Path())
	cfg := DefaultLocalConfig()
	_, err := os.Stat(ConfigPathIn(dir))
	if err == nil {
		cfg, err = LoadConfigIn(dir)
	} else if os.IsNotExist(err) {
		err = nil
	}
	if err != nil {
		http.Error(w, "Не удалось прочитать параметры просмотра PDF.", http.StatusInternalServerError)
		return
	}
	data, err := render(r.Context(), snapshotPath, anchor.Page, ingest.ToolConfig{PDFToPPM: cfg.Ingest.PDFToPPM, MuTool: cfg.Ingest.MuTool, Python: cfg.Ingest.Python})
	if err != nil {
		applog.Event("WARN", "graph.source.preview", applog.Fields{"page": anchor.Page, "error": err.Error()})
		http.Error(w, "Не удалось показать страницу. Откройте оригинальный PDF; проверьте pdftoppm, mutool или Python с PyMuPDF в параметрах импорта.", http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}
