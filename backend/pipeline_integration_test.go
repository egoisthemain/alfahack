package main

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"
)

func integrationApp(t *testing.T) *App {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to a demo database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = prepareDemo(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return &App{DB: pool, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}
func testApplication(t *testing.T, app *App, inn string) int64 {
	t.Helper()
	ctx := context.Background()
	c, err := getCompanyByINN(ctx, app.DB, inn)
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	a, err := createApplication(ctx, app.DB, c.ID, ApplicationRequest{Inn: inn, Product: "EXPRESS", Amount: 2000000, TermMonths: 24, Purpose: "Integration test", AnnualRevenue: 24000000, BKIConsent: &yes})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = app.DB.Exec(context.Background(), `DELETE FROM scoring_runs WHERE application_id=$1`, a.ID)
		_, _ = app.DB.Exec(context.Background(), `DELETE FROM application_events WHERE application_id=$1`, a.ID)
		_, _ = app.DB.Exec(context.Background(), `DELETE FROM applications WHERE application_id=$1`, a.ID)
	})
	if _, err = submitApplication(ctx, app.DB, a.ID); err != nil {
		t.Fatal(err)
	}
	return a.ID
}
func TestIntegrationModelFailureRequiresHuman(t *testing.T) {
	app := integrationApp(t)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer mock.Close()
	t.Setenv("MODEL_URL", mock.URL)
	id := testApplication(t, app, "7700000001")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a, err := app.process(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var d CreditDecision
	if err = json.Unmarshal(a.Decision, &d); err != nil {
		t.Fatal(err)
	}
	if a.Status != "MANUAL_REVIEW" || d.Mode != "points_fallback" || d.ModelID != "" {
		t.Fatalf("fallback must be explicit and manual: %s", a.Decision)
	}
	found := false
	for _, code := range d.Triggers {
		if code == "MR05" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing MR05")
	}
	if _, err = app.process(ctx, id); err == nil {
		t.Fatal("must reject repeated processing")
	}
	var n int
	if err = app.DB.QueryRow(ctx, `SELECT count(*) FROM scoring_runs WHERE application_id=$1`, id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("run count: %d, %v", n, err)
	}
}
func TestIntegrationStopPreventsModelCall(t *testing.T) {
	app := integrationApp(t)
	calls := 0
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
	defer mock.Close()
	t.Setenv("MODEL_URL", mock.URL)
	id := testApplication(t, app, "7700000007")
	a, err := app.process(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != "REJECTED" || calls != 0 {
		t.Fatalf("status=%s model calls=%d", a.Status, calls)
	}
}

func TestIntegrationAuditRegistrationEvidence(t *testing.T) {
	app := integrationApp(t)
	id := testApplication(t, app, "7700000009")
	if _, err := app.process(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	token := "integration-audit-registration"
	sessions.Store(token, demoSession{Role: "analyst", Expires: time.Now().Add(time.Minute)})
	defer sessions.Delete(token)
	r := httptest.NewRequest("GET", "/audit", nil)
	r.SetPathValue("id", strconv.FormatInt(id, 10))
	r.AddCookie(&http.Cookie{Name: "alfahack_session", Value: token})
	w := httptest.NewRecorder()
	app.auditHandler(w, r)
	if w.Code != 200 {
		t.Fatalf("audit %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		Verification struct {
			AgeMonths int    `json:"age_months"`
			AgeStop   bool   `json:"age_stop_triggered"`
			Matches   bool   `json:"registration_matches_current_db"`
			CheckedAt string `json:"checked_at"`
		} `json:"verification"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Verification.AgeStop || result.Verification.AgeMonths >= 3 || !result.Verification.Matches || result.Verification.CheckedAt == "" {
		t.Fatalf("invalid evidence: %s", w.Body.String())
	}
	sessions.Store(token, demoSession{Role: "client", Expires: time.Now().Add(time.Minute)})
	w = httptest.NewRecorder()
	app.auditHandler(w, r)
	if w.Code != 403 {
		t.Fatalf("client audit status %d", w.Code)
	}
}
