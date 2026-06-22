package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHTTPJSONErr_EscapesAndSetsContentType pins the httpJSONErr contract in one
// place (rather than per-handler): the {"error": msg} body is always valid JSON
// with the message faithfully escaped — even with quotes, backslashes, control
// chars, or unicode — and Content-Type is application/json. Several handlers
// rely on this; #552 was a hand-built literal that did not.
func TestHTTPJSONErr_EscapesAndSetsContentType(t *testing.T) {
	for _, msg := range []string{
		`plain message`,
		`has a "quote"`,
		`has a \backslash`,
		"tab\tand\nnewline",
		"null\x00byte",
		`unicode résumé 🚀`,
	} {
		t.Run(msg, func(t *testing.T) {
			w := httptest.NewRecorder()
			httpJSONErr(w, http.StatusBadRequest, msg)

			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", w.Code)
			}
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("body not valid JSON: %v (body=%q)", err, w.Body.String())
			}
			if body.Error != msg {
				t.Errorf("error field = %q, want %q", body.Error, msg)
			}
		})
	}
}
