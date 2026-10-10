package webapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func (a *App) mapViewPresetsRoute(w http.ResponseWriter, r *http.Request, dir string, actor workspace.Actor, parts []string) bool {
	if len(parts) != 7 || parts[6] != "view-presets" {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		writeError(w, 405, "Ожидается GET или POST")
		return true
	}
	if r.URL.RawQuery != "" {
		writeError(w, 400, "Параметры запроса не поддерживаются")
		return true
	}
	release, ok := a.reserveQuery(w, actor)
	if !ok {
		return true
	}
	defer release()
	var result any
	var err error
	if r.Method == http.MethodGet {
		result, err = mem.ReadWebMapViewPresetsIn(r.Context(), dir, actor.ID, parts[5])
	} else {
		var input mem.WebMapViewPresetInput
		if !decodeMapViewPreset(w, r, &input) {
			return true
		}
		result, err = mem.SaveWebMapViewPresetIn(r.Context(), dir, actor.ID, parts[5], input, func() error {
			base, e := a.manager.Resolve(r.Context(), actor, parts[3])
			if e != nil {
				return e
			}
			return a.checkStorage(r.Context(), actor, base)
		})
	}
	if err != nil {
		a.mapViewStateError(w, err)
		return true
	}
	writeJSON(w, 200, result)
	return true
}

// Explicit saves carry a complete state, not a patch. Enforce exact field
// names, no duplicate keys/nulls, and Unicode validity before Go's permissive
// JSON decoder can replace an invalid string or match a case-insensitive key.
func decodeMapViewPreset(w http.ResponseWriter, r *http.Request, input *mem.WebMapViewPresetInput) bool {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		writeError(w, 415, "Ожидается JSON")
		return false
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if err != nil {
		var max *http.MaxBytesError
		if errors.As(err, &max) {
			writeError(w, 413, "Тело представления слишком велико")
		} else {
			writeError(w, 400, "Некорректное тело запроса")
		}
		return false
	}
	if !utf8.Valid(data) || !mapViewJSONUnicode(data) || !validMapViewJSONShapeFields(data, "revision map_revision request_id state operation id name") {
		writeError(w, 400, "Ожидается полный JSON личного представления без повторяющихся полей")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(input); err != nil {
		writeError(w, 400, "Некорректное тело запроса")
		return false
	}
	return true
}
