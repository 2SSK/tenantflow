package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestLoggerLogsStatusAndPath(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()

	RequestLogger(log, next).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d", rec.Code)
	}

	out := buf.String()
	if !bytes.Contains([]byte(out), []byte("/status")) {
		t.Errorf("expected path in log, got %q", out)
	}
	if !bytes.Contains([]byte(out), []byte("status=201")) {
		t.Errorf("expected status=201 in log, got %q", out)
	}
}
