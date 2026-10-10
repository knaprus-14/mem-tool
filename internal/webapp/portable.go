package webapp

import (
	"errors"
	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

var errPortableModels = errors.New("модели архива не разрешены администратором")

func (a *App) validatePortableModels(cfg *mem.Config) error {
	settings := workspace.SettingsFromConfig(cfg)
	if !a.modelAllowed("embedding", settings.Backend, settings.EmbeddingModel) || (settings.AnswerModel != "" && !a.modelAllowed("answer", "ollama", settings.AnswerModel)) {
		return errPortableModels
	}
	return nil
}

func (a *App) importPortable(w http.ResponseWriter, r *http.Request, actor workspace.Actor) {
	release, ok := a.reserveQuery(w, actor)
	if !ok {
		return
	}
	defer release()
	f, err := os.CreateTemp("", "mem-portable-upload-*.zip")
	if err != nil {
		a.fail(w, err)
		return
	}
	defer os.Remove(f.Name())
	r.Body = http.MaxBytesReader(w, r.Body, mem.MaxPortableBytes)
	_, err = io.Copy(f, r.Body)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		writeError(w, 413, "Архив не принят: максимум 2 ГиБ")
		return
	}
	_, used, err := a.storageUsage(r.Context(), actor, "")
	if err != nil {
		a.fail(w, err)
		return
	}
	base, manifest, err := a.manager.ImportPortable(r.Context(), actor, r.URL.Query().Get("name"), f.Name(), a.cfg.MaxWorkspaceBytes, a.cfg.MaxUserBytes-used, a.validatePortableModels)
	if err != nil {
		if errors.Is(err, errPortableModels) {
			writeError(w, 400, "Модели архива не разрешены на сервере. Администратор должен добавить их в список models перед импортом; векторы автоматически не заменяются.")
			return
		}
		a.maintenanceError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"workspace": a.workspaceView(base), "missing_sources": manifest.MissingSources})
}
func (a *App) exportPortable(w http.ResponseWriter, r *http.Request, actor workspace.Actor, id string) {
	base, err := a.manager.Resolve(r.Context(), actor, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	release, ok := a.reserveQuery(w, actor)
	if !ok {
		return
	}
	defer release()
	path, manifest, cleanup, err := mem.PreparePortableDatabaseIn(r.Context(), filepath.Join(base.Root, mem.MemDirName), a.cfg.Local)
	defer cleanup()
	if err != nil {
		a.maintenanceError(w, err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		a.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="mem-database.zip"`)
	w.Header().Set("X-Mem-Missing-Sources", strconv.Itoa(manifest.MissingSources))
	http.ServeContent(w, r, "mem-database.zip", info.ModTime(), f)
}
