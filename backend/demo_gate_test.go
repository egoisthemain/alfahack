package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDemoGate(t *testing.T) {
	t.Setenv("DEMO_ACCESS_PASSWORD", "test-only-password")
	h := demoGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, tc := range []struct {
		path, user, password string
		status               int
	}{
		{"/", "", "", 401},
		{"/demo/login", "demo", "wrong", 401},
		{"/", "analyst", "test-only-password", 401},
		{"/", "demo", "test-only-password", 204},
		{"/health", "", "", 204},
	} {
		r := httptest.NewRequest("GET", tc.path, nil)
		if tc.user != "" {
			r.SetBasicAuth(tc.user, tc.password)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: got %d, want %d", tc.path, w.Code, tc.status)
		}
	}
}

func TestDemoGateDisabledLocally(t *testing.T) {
	t.Setenv("DEMO_ACCESS_PASSWORD", "")
	w := httptest.NewRecorder()
	demoGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
}
