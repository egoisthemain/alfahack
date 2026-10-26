package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"os"
)

// A shared gate for a hosted synthetic demo; role selection remains a simulation.
func demoGate(next http.Handler) http.Handler {
	password := os.Getenv("DEMO_ACCESS_PASSWORD")
	if password == "" {
		return next
	}
	expected := sha256.Sum256([]byte(password))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" && r.Method == http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		user, supplied, ok := r.BasicAuth()
		actual := sha256.Sum256([]byte(supplied))
		if !ok || user != "demo" || subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Alfahack demo", charset="UTF-8"`)
			http.Error(w, "Demo access required", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
