package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakeDB struct{ err error }

func (db fakeDB) Ping(context.Context) error { return db.err }

func TestHealthEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name   string
		db     fakeDB
		path   string
		status int
	}{
		{"live without database", fakeDB{errors.New("down")}, "/health/live", http.StatusOK},
		{"ready with database", fakeDB{}, "/health/ready", http.StatusOK},
		{"not ready without database", fakeDB{errors.New("down")}, "/health/ready", http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			Handler(tc.db, time.Second).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.status)
			}
		})
	}
}
