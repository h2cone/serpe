package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/h2cone/serpe/internal/jsonvalue"
	"github.com/h2cone/serpe/runtime/sessions"
)

const (
	mutationBodyLimit = int64(64 << 10)
	runBodyLimit      = int64(8 << 20)
)

func (s *Server) readJSONObject(w http.ResponseWriter, r *http.Request, limit int64, allowEmpty bool) (jsonvalue.Value, bool) {
	if err := validateJSONHeaders(r, allowEmpty); err != nil {
		badBody(w, "invalid_content_headers")
		return jsonvalue.Value{}, false
	}
	controller, err := s.setReadDeadline(w)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "deadline_unsupported")
		return jsonvalue.Value{}, false
	}
	reader := io.Reader(r.Body)
	if reader == nil {
		reader = bytes.NewReader(nil)
	}
	raw, readErr := io.ReadAll(io.LimitReader(reader, limit+1))
	if err := controller.SetReadDeadline(time.Time{}); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "deadline_unsupported")
		return jsonvalue.Value{}, false
	}
	if readErr != nil {
		badBody(w, "invalid_body")
		return jsonvalue.Value{}, false
	}
	if int64(len(raw)) > limit {
		badBody(w, "body_too_large")
		return jsonvalue.Value{}, false
	}
	if len(raw) == 0 {
		if allowEmpty {
			return jsonvalue.Value{Kind: jsonvalue.KindObject, Object: []jsonvalue.Member{}}, true
		}
		badBody(w, "body_required")
		return jsonvalue.Value{}, false
	}
	value, err := jsonvalue.ParseObject(raw, jsonvalue.Limits{
		MaxDepth: 64, MaxNodes: 65_536, MaxNumberBytes: 128, MaxExponent: 1_000, MaxScale: 1_024,
	})
	if err != nil {
		badBody(w, "invalid_json")
		return jsonvalue.Value{}, false
	}
	return value, true
}

func validateJSONHeaders(r *http.Request, allowEmpty bool) error {
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 || strings.ContainsRune(contentTypes[0], ',') {
		if allowEmpty && r.ContentLength == 0 && len(contentTypes) == 0 && len(r.TransferEncoding) == 0 {
			return nil
		}
		return errors.New("one Content-Type is required")
	}
	mediaType, parameters, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return errors.New("Content-Type must be application/json")
	}
	for key, value := range parameters {
		if !strings.EqualFold(key, "charset") || !strings.EqualFold(value, "utf-8") {
			return errors.New("unsupported Content-Type parameter")
		}
	}
	encodings := r.Header.Values("Content-Encoding")
	if len(encodings) > 1 || len(encodings) == 1 && (!strings.EqualFold(encodings[0], "identity") || strings.ContainsRune(encodings[0], ',')) {
		return errors.New("Content-Encoding must be identity")
	}
	return nil
}

func badBody(w http.ResponseWriter, code string) {
	w.Header().Set("Connection", "close")
	writeAPIError(w, http.StatusBadRequest, code)
}

func (s *Server) rejectBody(w http.ResponseWriter, r *http.Request) bool {
	if len(r.TransferEncoding) != 0 || r.ContentLength > 0 {
		badBody(w, "body_not_allowed")
		return true
	}
	if r.Body == nil || r.Body == http.NoBody {
		return false
	}
	controller, err := s.setReadDeadline(w)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "deadline_unsupported")
		return true
	}
	var one [1]byte
	n, readErr := r.Body.Read(one[:])
	if err := controller.SetReadDeadline(time.Time{}); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "deadline_unsupported")
		return true
	}
	if n != 0 || readErr != nil && !errors.Is(readErr, io.EOF) {
		badBody(w, "body_not_allowed")
		return true
	}
	return false
}

func (s *Server) setReadDeadline(w http.ResponseWriter) (*http.ResponseController, error) {
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(s.readTimeout)); err != nil {
		return nil, err
	}
	return controller, nil
}

func requireFields(value jsonvalue.Value, allowed ...string) error {
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for _, member := range value.Object {
		if _, ok := set[member.Key]; !ok {
			return fmt.Errorf("unknown field")
		}
	}
	return nil
}

func optionalString(value jsonvalue.Value, key string) (string, bool, error) {
	field, present := value.Lookup(key)
	if !present {
		return "", false, nil
	}
	if field.Kind != jsonvalue.KindString {
		return "", true, fmt.Errorf("%s must be a string", key)
	}
	return field.String, true, nil
}

func parsePageQuery(r *http.Request, cursorKey string) (cursor string, present bool, limit int, err error) {
	if strings.ContainsRune(r.URL.RawQuery, '%') {
		return "", false, 0, errors.New("escaped query is not canonical")
	}
	query := r.URL.Query()
	for key, values := range query {
		if key != cursorKey && key != "limit" || len(values) != 1 {
			return "", false, 0, errors.New("unknown or repeated query parameter")
		}
	}
	limit = 100
	if values, ok := query["limit"]; ok {
		text := values[0]
		if text == "" || text[0] == '0' || len(text) > 3 {
			return "", false, 0, errors.New("invalid limit")
		}
		parsed, parseErr := strconv.Atoi(text)
		if parseErr != nil || parsed < 1 || parsed > 100 || strconv.Itoa(parsed) != text {
			return "", false, 0, errors.New("invalid limit")
		}
		limit = parsed
	}
	if values, ok := query[cursorKey]; ok {
		if values[0] == "" || len(values[0]) > maxCursorWireBytes {
			return "", false, 0, errors.New("invalid cursor")
		}
		cursor, present = values[0], true
	}
	return cursor, present, limit, nil
}

func rejectQuery(r *http.Request) error {
	if r.URL.RawQuery != "" {
		return errors.New("query parameters are not allowed")
	}
	return nil
}

func validTitle(title string) bool {
	return len(title) <= 4<<10 && utf8.ValidString(title) && !hasControl(title)
}

func hasControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func (s *Server) normalizeWorkingDir(ctx context.Context, value string, present bool) (string, error) {
	if !present {
		return s.cwd, nil
	}
	if value == "" || !utf8.ValidString(value) || len(value) > 32<<10 || hasControl(value) {
		return "", errors.New("invalid cwd")
	}
	if !filepath.IsAbs(value) {
		if filepath.VolumeName(value) != "" || runtime.GOOS == "windows" && strings.ContainsRune(value, ':') {
			return "", errors.New("ambiguous cwd")
		}
		value = filepath.Join(s.cwd, value)
	}
	value = filepath.Clean(value)
	if err := s.validateCWD(ctx, value); err != nil {
		return "", err
	}
	return value, nil
}

func validRouteID(id string) bool { return sessions.ValidID(id) }

func (s *Server) generatedID() (id string, err error) {
	if s.newID != nil {
		defer func() {
			if recover() != nil {
				id = ""
				err = errors.New("ID generator panic")
			}
		}()
		id = s.newID()
	} else {
		var random [16]byte
		if _, err = io.ReadFull(s.random, random[:]); err != nil {
			return "", err
		}
		id = base64.RawURLEncoding.EncodeToString(random[:])
	}
	if !sessions.ValidID(id) {
		return "", errors.New("ID generator returned invalid ID")
	}
	return id, nil
}
