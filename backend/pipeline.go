package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"time"
)

var errState = errors.New("invalid application state")

type FullApplication struct {
	Application
	INN           string          `json:"inn"`
	CompanyName   string          `json:"company_name"`
	Product       string          `json:"product"`
	Amount        int64           `json:"amount"`
	TermMonths    int             `json:"term_months"`
	Purpose       string          `json:"purpose"`
	AnnualRevenue int64           `json:"annual_revenue"`
	Consent       bool            `json:"bki_consent"`
	EquipmentCost *int64          `json:"equipment_cost"`
	Document      *string         `json:"supplier_document_id"`
	SubmittedAt   *time.Time      `json:"submitted_at"`
	Decision      json.RawMessage `json:"decision"`
	ScoringError  *string         `json:"scoring_error"`
}

func (app *App) fullApplication(ctx context.Context, id int64) (FullApplication, error) {
	a := FullApplication{}
	err := app.DB.QueryRow(ctx, `SELECT a.application_id,a.company_id,a.status,a.created_at,c.inn,c.name,
        a.product,a.amount,a.term_months,a.purpose,a.declared_annual_revenue,a.bki_consent,a.equipment_cost,a.supplier_document_id,a.submitted_at,a.decision,a.scoring_error
        FROM applications a JOIN companies c USING(company_id) WHERE application_id=$1`, id).Scan(
		&a.ID, &a.CompanyID, &a.Status, &a.CreatedAt, &a.INN, &a.CompanyName, &a.Product, &a.Amount, &a.TermMonths, &a.Purpose, &a.AnnualRevenue, &a.Consent, &a.EquipmentCost, &a.Document, &a.SubmittedAt, &a.Decision, &a.ScoringError)
	if err == nil && a.Status == "MANUAL_REVIEW" {
		// Supply review terms for existing decisions made before limits were
		// calculated for grade E. Keep the original recommendation and audit.
		var d CreditDecision
		if json.Unmarshal(a.Decision, &d) == nil && d.Offer == nil && d.ReviewTerms == nil && d.PD != nil && d.Grade != "" {
			var raw []byte
			if err = app.DB.QueryRow(ctx, `SELECT source_snapshot FROM applications WHERE application_id=$1`, id).Scan(&raw); err != nil {
				return a, err
			}
			var snapshot map[string]any
			if err = json.Unmarshal(raw, &snapshot); err != nil {
				return a, err
			}
			financial := block(snapshot, "financial_profile")
			revenue := float64(a.AnnualRevenue)
			if confirmed, ok := financial["verified_annual_revenue"].(float64); ok {
				revenue = confirmed
			}
			terms := decisionFor(a.request(), *d.PD, d.Grade, num(block(snapshot, "bki_profile"), "existing_monthly_payment_rub"), revenue)
			d.Limit, d.ReviewTerms = terms.Limit, terms.ReviewTerms
			a.Decision, err = json.Marshal(d)
		}
	}
	return a, err
}
func (a FullApplication) request() ApplicationRequest {
	return ApplicationRequest{Inn: a.INN, Product: a.Product, Amount: a.Amount, TermMonths: a.TermMonths, Purpose: a.Purpose, AnnualRevenue: a.AnnualRevenue, BKIConsent: &a.Consent, EquipmentCost: a.EquipmentCost, SupplierDocumentID: a.Document}
}

// Every status change and its event are atomic. Network calls never hold a row lock.
func (app *App) transition(ctx context.Context, id int64, from, to, actor, code string, decision any) error {
	tx, err := app.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var raw any
	if decision != nil {
		b, e := json.Marshal(decision)
		if e != nil {
			return e
		}
		raw = string(b)
	}
	tag, err := tx.Exec(ctx, `UPDATE applications SET status=$3,decision=COALESCE($4::jsonb,decision),scoring_error=NULL WHERE application_id=$1 AND status=$2`, id, from, to, raw)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errState
	}
	_, err = tx.Exec(ctx, `INSERT INTO application_events(application_id,from_status,to_status,actor,reason_code) VALUES($1,$2,$3,$4,$5)`, id, from, to, actor, code)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (app *App) profiles(ctx context.Context, id int64, consent bool) (map[string]any, error) {
	// No BKI lookup is performed at all without consent.
	s := map[string]any{}
	kinds := []string{"registry", "business_checks", "financial_profile"}
	if consent {
		kinds = append(kinds, "bki_profile")
	}
	for _, k := range kinds {
		var raw []byte
		err := app.DB.QueryRow(ctx, `SELECT snapshot FROM source_profiles WHERE company_id=$1 AND kind=$2`, id, k).Scan(&raw)
		if err != nil {
			return nil, fmt.Errorf("missing %s: %w", k, err)
		}
		var v map[string]any
		if err = json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		v["company_id"] = strconv.FormatInt(id, 10)
		if k == "registry" {
			s["company"] = v
		} else {
			s[k] = v
		}
	}
	return s, nil
}
func predictSources(ctx context.Context, s map[string]any) (map[string]any, error) {
	body, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	base := os.Getenv("MODEL_URL")
	if base == "" {
		base = "http://127.0.0.1:8001"
	}
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/predict-sources", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("model HTTP %d: %s", response.StatusCode, string(raw))
	}
	var out map[string]any
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	pd, ok := out["pd"].(float64)
	if !ok || math.IsNaN(pd) || math.IsInf(pd, 0) || pd < 0 || pd > 1 {
		return nil, errors.New("invalid model PD")
	}
	if text(out, "feature_schema_version") != "alpha-v1.1-unified-features-1" || text(out, "model_id") == "" {
		return nil, errors.New("invalid model metadata")
	}
	return out, nil
}
func (app *App) process(ctx context.Context, id int64) (FullApplication, error) {
	a, err := app.fullApplication(ctx, id)
	if err != nil {
		return a, err
	}
	if a.Status != "SUBMITTED" && a.Status != "NEED_INFO" {
		return a, errState
	}
	if err = app.transition(ctx, id, a.Status, "DATA_CHECK", "SYSTEM", "CHECK_STARTED", nil); err != nil {
		return a, err
	}
	need := CreditDecision{Decision: "NEED_INFO", Mode: "not_scored", Reasons: []Reason{}, Triggers: []string{}, PolicyVersion: "2026-10-22.1"}
	if !a.Consent {
		need.Reasons = append(need.Reasons, Reason{"CONSENT_REQUIRED", "Нужно согласие на запрос кредитной истории"})
	}
	if a.Product == "EQUIPMENT" {
		valid := false
		if a.Document != nil {
			_ = app.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM demo_documents WHERE document_id::text=$1 AND company_id=$2)`, *a.Document, a.CompanyID).Scan(&valid)
		}
		if !valid {
			need.Reasons = append(need.Reasons, Reason{"N01", "Загрузите договор поставки или счёт на оборудование"})
		}
	}
	if len(need.Reasons) > 0 {
		err = app.transition(ctx, id, "DATA_CHECK", "NEED_INFO", "SYSTEM", "NEED_INFO", need)
		if err != nil {
			return a, err
		}
		return app.fullApplication(ctx, id)
	}
	s, err := app.profiles(ctx, a.CompanyID, a.Consent)
	if err != nil {
		d := CreditDecision{Decision: "MANUAL_REVIEW", Mode: "not_scored", Reasons: []Reason{{"SOURCE_DATA_MISSING", "Не хватает сохранённых профилей источников"}}, Triggers: []string{"MR04"}, PolicyVersion: "2026-10-22.1"}
		if e := app.transition(ctx, id, "DATA_CHECK", "MANUAL_REVIEW", "SYSTEM", "SOURCE_DATA_MISSING", d); e != nil {
			return a, e
		}
		return app.fullApplication(ctx, id)
	}
	c, err := getCompanyByINN(ctx, app.DB, a.INN)
	if err != nil {
		return a, err
	}
	at := time.Now().UTC()
	if a.SubmittedAt != nil {
		at = *a.SubmittedAt
	}
	// Registry fields must reflect the current persisted company, not a separate fixture copy.
	reg := block(s, "company")
	reg["registration_date"] = c.RegistrationDate.Format("2006-01-02")
	reg["status"] = c.Status
	reg["okved"] = c.OKVED
	reg["legal_form"] = c.LegalForm
	reg["inn"] = c.INN
	req := a.request()
	s["application"] = map[string]any{"application_id": strconv.FormatInt(a.ID, 10), "company_id": strconv.FormatInt(a.CompanyID, 10), "inn": a.INN, "product": a.Product, "amount": a.Amount, "term_months": a.TermMonths, "purpose": a.Purpose, "declared_annual_revenue": a.AnnualRevenue, "bki_consent": a.Consent, "equipment_cost": a.EquipmentCost, "supplier_document_id": a.Document, "submitted_at": at.Format(time.RFC3339), "status": "SCORING"}
	snapshot, _ := json.Marshal(s)
	if _, err = app.DB.Exec(ctx, `UPDATE applications SET source_snapshot=$2::jsonb WHERE application_id=$1`, id, string(snapshot)); err != nil {
		return a, err
	}
	if err = app.transition(ctx, id, "DATA_CHECK", "SCORING", "SYSTEM", "SCORING_STARTED", nil); err != nil {
		return a, err
	}
	stops := registryStops(c, block(s, "bki_profile"), block(s, "business_checks"), at)
	d := CreditDecision{Decision: "REJECTED", Mode: "not_scored", Reasons: stops, Triggers: []string{}, PolicyVersion: "2026-10-22.1"}
	var model map[string]any
	if len(stops) == 0 {
		model, err = predictSources(ctx, s)
		existing := num(block(s, "bki_profile"), "existing_monthly_payment_rub")
		// Use confirmed revenue when available, never inflate limits using the form.
		selectedRevenue := float64(a.AnnualRevenue)
		if verified, ok := block(s, "financial_profile")["verified_annual_revenue"].(float64); ok {
			selectedRevenue = verified
		}
		if err == nil {
			pd := num(model, "pd")
			d = decisionFor(req, pd, pdGrade(pd), existing, selectedRevenue)
			d.ModelID = text(model, "model_id")
			if d.Grade == "C" || d.Grade == "D" || d.Grade == "E" {
				d.Reasons = modelReasons(model)
			}
		} else {
			app.Logger.Error("inference fallback", "application_id", id, "error", err)
			pointsRequest := req
			pointsRequest.AnnualRevenue = int64(selectedRevenue)
			score, reasons := pointsScore(c, pointsRequest, block(s, "bki_profile"), block(s, "business_checks"), at)
			grade := "E"
			pd := 0.
			switch {
			case score >= 75:
				grade = "A"
				pd = .02
			case score >= 60:
				grade = "B"
				pd = .06
			case score >= 45:
				grade = "C"
				pd = .115
			case score >= 30:
				grade = "D"
				pd = .20
			}
			d = decisionFor(req, pd, grade, existing, selectedRevenue)
			d.Score = &score
			d.Mode = "points_fallback"
			d.Reasons = reasons
			if grade == "E" {
				d.PD = nil
			}
			if d.Decision != "MANUAL_REVIEW" {
				d.Recommendation = d.Decision
				d.Decision = "MANUAL_REVIEW"
			}
			d.Triggers = append(d.Triggers, "MR05")
			model = map[string]any{"error": err.Error(), "mode": "points_fallback"}
		}
	}
	// Persist the audit run and terminal scoring transition in one transaction.
	tx, err := app.DB.Begin(ctx)
	if err != nil {
		return a, err
	}
	defer tx.Rollback(ctx)
	raw, _ := json.Marshal(d)
	mr, _ := json.Marshal(model)
	tag, err := tx.Exec(ctx, `UPDATE applications SET status=$2,decision=$3::jsonb,scoring_error=NULL WHERE application_id=$1 AND status='SCORING'`, id, d.Decision, string(raw))
	if err != nil {
		return a, err
	}
	if tag.RowsAffected() != 1 {
		return a, errState
	}
	_, err = tx.Exec(ctx, `INSERT INTO scoring_runs(application_id,sources,model_response,decision) VALUES($1,$2::jsonb,$3::jsonb,$4::jsonb)`, id, string(snapshot), string(mr), string(raw))
	if err != nil {
		return a, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO application_events(application_id,from_status,to_status,actor,reason_code) VALUES($1,'SCORING',$2,'SYSTEM','POLICY_RESULT')`, id, d.Decision)
	if err != nil {
		return a, err
	}
	if err = tx.Commit(ctx); err != nil {
		return a, err
	}
	return app.fullApplication(ctx, id)
}

func (app *App) endpointError(w http.ResponseWriter, err error) {
	status, code, message := 500, "INTERNAL_ERROR", "Не удалось выполнить действие"
	if errors.Is(err, pgx.ErrNoRows) {
		status, code, message = 404, "APPLICATION_NOT_FOUND", "Заявка не найдена"
	} else if errors.Is(err, errState) {
		status, code, message = 409, "INVALID_STATE", "Действие недоступно в текущем состоянии"
	} else {
		app.Logger.Error("request failed", "error", err)
	}
	_ = writeJSON(w, status, ErrorMsg{code, message})
}
func (app *App) detailHandler(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	a, err := app.fullApplication(r.Context(), id)
	if err != nil {
		app.endpointError(w, err)
		return
	}
	_ = writeJSON(w, 200, a)
}
func (app *App) processHandler(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	a, err := app.process(ctx, id)
	if err != nil {
		app.endpointError(w, err)
		return
	}
	_ = writeJSON(w, 200, a)
}
func (app *App) historyHandler(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	rows, err := app.DB.Query(r.Context(), `SELECT event_id,from_status,to_status,actor,reason_code,created_at FROM application_events WHERE application_id=$1 ORDER BY event_id`, id)
	if err != nil {
		app.endpointError(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var eid int64
		var from, to, actor string
		var code *string
		var at time.Time
		if err = rows.Scan(&eid, &from, &to, &actor, &code, &at); err != nil {
			app.endpointError(w, err)
			return
		}
		out = append(out, map[string]any{"event_id": eid, "from_status": from, "to_status": to, "actor": actor, "reason_code": code, "created_at": at})
	}
	if err = rows.Err(); err != nil {
		app.endpointError(w, err)
		return
	}
	_ = writeJSON(w, 200, out)
}
func (app *App) listHandler(w http.ResponseWriter, r *http.Request) {
	s, _ := session(r)
	rows, err := app.DB.Query(r.Context(), `SELECT application_id FROM applications WHERE ($1='analyst' OR company_id=$2) ORDER BY application_id DESC LIMIT 100`, s.Role, s.CompanyID)
	if err != nil {
		app.endpointError(w, err)
		return
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			app.endpointError(w, err)
			return
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		app.endpointError(w, err)
		return
	}
	out := []FullApplication{}
	for _, id := range ids {
		a, e := app.fullApplication(r.Context(), id)
		if e != nil {
			app.endpointError(w, e)
			return
		}
		out = append(out, a)
	}
	_ = writeJSON(w, 200, out)
}
func (app *App) companiesHandler(w http.ResponseWriter, r *http.Request) {
	var fixtures []map[string]any
	_ = json.Unmarshal(demoFixtures, &fixtures)
	out := []map[string]any{}
	for _, s := range fixtures {
		c := block(s, "company")
		out = append(out, map[string]any{"inn": c["inn"], "name": c["name"], "demo_request": s["demo_request"]})
	}
	_ = writeJSON(w, 200, out)
}
