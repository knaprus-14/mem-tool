package webapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/knaprus-14/mem-tool/internal/workspace"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func (a *App) mapViewStateRoute(w http.ResponseWriter, r *http.Request, dir string, actor workspace.Actor, parts []string) bool {
	if len(parts) != 7 || parts[6] != "view-state" {
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
	var result mem.WebMapViewStateResult
	var err error
	if r.Method == http.MethodGet {
		result, err = mem.ReadWebMapViewStateIn(r.Context(), dir, actor.ID, parts[5])
	} else {
		var input mem.WebMapViewStateInput
		if !decodeMapViewState(w, r, &input) {
			return true
		}
		result, err = mem.SaveWebMapViewStateIn(r.Context(), dir, actor.ID, parts[5], input, func() error {
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

func (a *App) mapViewStateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, workspace.ErrStorageLimit):
		a.fail(w, err)
	case errors.Is(err, mem.ErrWebMapViewStateInvalid):
		writeError(w, 400, err.Error())
	case errors.Is(err, mem.ErrWebMapViewStateNotFound):
		writeError(w, 404, err.Error())
	case errors.Is(err, mem.ErrWebMapViewStateConflict), errors.Is(err, mem.ErrWebMapViewStateStale), errors.Is(err, mem.ErrWebMapViewStateRequest):
		writeError(w, 409, err.Error())
	case errors.Is(err, mem.ErrWebMapViewStateLimit):
		writeError(w, 413, err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		a.queryError(w, err)
	default:
		writeError(w, 500, "Не удалось прочитать или сохранить личное представление карты")
	}
}

// Explicit saves carry a complete state, not a patch. Enforce exact field
// names, no duplicate keys/nulls, and Unicode validity before Go's permissive
// JSON decoder can replace an invalid string or match a case-insensitive key.
func decodeMapViewState(w http.ResponseWriter, r *http.Request, input *mem.WebMapViewStateInput) bool {
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
	if !utf8.Valid(data) || !mapViewJSONUnicode(data) || !validMapViewJSONShape(data) {
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

func validMapViewJSONShape(data []byte) bool {
	return validMapViewJSONShapeFields(data, "revision map_revision request_id state")
}

func validMapViewJSONShapeFields(data []byte, fields string) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var object func(fields string, depth int) bool
	object = func(fields string, depth int) bool {
		token, err := decoder.Token()
		if err != nil || token != json.Delim('{') {
			return false
		}
		allowed := map[string]bool{}
		for _, key := range strings.Fields(fields) {
			allowed[key] = true
		}
		seen := map[string]bool{}
		for decoder.More() {
			token, err = decoder.Token()
			key, ok := token.(string)
			if err != nil || !ok || !allowed[key] || seen[key] {
				return false
			}
			seen[key] = true
			if depth == 0 && key == "state" {
				if !object("display_mode layout selected_node_id collapsed scale scroll_x scroll_y table_sort table_descending table_query table_page", 1) {
					return false
				}
				continue
			}
			if depth == 1 && key == "collapsed" {
				token, err = decoder.Token()
				if err != nil || token != json.Delim('[') {
					return false
				}
				count := 0
				for decoder.More() {
					token, err = decoder.Token()
					value, ok := token.(string)
					count++
					if err != nil || !ok || len(value) > 256 || count > mem.MaxWebMapViewStateCollapsed {
						return false
					}
				}
				token, err = decoder.Token()
				if err != nil || token != json.Delim(']') {
					return false
				}
				continue
			}
			token, err = decoder.Token()
			if err != nil || token == nil {
				return false
			}
			if _, ok := token.(json.Delim); ok {
				return false
			}
			if value, ok := token.(string); ok && strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' }) {
				return false
			}
		}
		token, err = decoder.Token()
		return err == nil && token == json.Delim('}') && len(seen) == len(allowed)
	}
	if !object(fields, 0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func mapViewJSONUnicode(data []byte) bool {
	quoted := false
	for i := 0; i < len(data); i++ {
		if data[i] == '"' {
			quoted = !quoted
		} else if quoted && data[i] == '\\' {
			i++
			if i >= len(data) {
				return false
			}
			if data[i] != 'u' {
				continue
			}
			if i+4 >= len(data) {
				return false
			}
			n, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
			if err != nil {
				return false
			}
			i += 4
			if n >= 0xDC00 && n <= 0xDFFF {
				return false
			}
			if n >= 0xD800 && n <= 0xDBFF {
				if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
					return false
				}
				low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
				if err != nil || low < 0xDC00 || low > 0xDFFF {
					return false
				}
				i += 6
			}
		}
	}
	return true
}
