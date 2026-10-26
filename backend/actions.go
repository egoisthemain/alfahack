package main

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (app *App) downloadHandler(w http.ResponseWriter, r *http.Request) {
	s, _ := session(r)
	var companyID int64
	var name string
	var data []byte
	err := app.DB.QueryRow(r.Context(), `SELECT company_id,name,content FROM demo_documents WHERE document_id::text=$1`, r.PathValue("id")).Scan(&companyID, &name, &data)
	if err != nil {
		app.endpointError(w, err)
		return
	}
	if s.Role != "analyst" && s.CompanyID != companyID {
		_ = writeJSON(w, 403, ErrorMsg{"FORBIDDEN", "Нет доступа к документу"})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	_, _ = w.Write(data)
}

func (app *App) auditHandler(w http.ResponseWriter, r *http.Request) {
	s, _ := session(r)
	if s.Role != "analyst" {
		_ = writeJSON(w, 403, ErrorMsg{"FORBIDDEN", "Аудит доступен сотруднику"})
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var sources, model, decision []byte
	err := app.DB.QueryRow(r.Context(), `SELECT sources,model_response,decision FROM scoring_runs WHERE application_id=$1 ORDER BY run_id DESC LIMIT 1`, id).Scan(&sources, &model, &decision)
	if err != nil {
		app.endpointError(w, err)
		return
	}
	var snapshot map[string]any
	if err = json.Unmarshal(sources, &snapshot); err != nil {
		app.endpointError(w, err)
		return
	}
	registry := block(snapshot, "company")
	application := block(snapshot, "application")
	registered, regErr := time.Parse("2006-01-02", text(registry, "registration_date"))
	checkedAt, atErr := time.Parse(time.RFC3339, text(application, "submitted_at"))
	verification := map[string]any{"source": "Синтетические данные PostgreSQL; снимок на момент рассмотрения", "registration_date": text(registry, "registration_date"), "checked_at": text(application, "submitted_at"), "minimum_age_months": 3}
	if regErr == nil && atErr == nil {
		months := ageMonths(registered, checkedAt)
		verification["age_months"] = months
		verification["age_stop_triggered"] = months < 3
	}
	current, err := getCompanyByINN(r.Context(), app.DB, text(registry, "inn"))
	if err != nil {
		app.endpointError(w, err)
		return
	}
	verification["current_company"] = current
	verification["registration_matches_current_db"] = current.RegistrationDate.Format("2006-01-02") == text(registry, "registration_date")
	_ = writeJSON(w, 200, map[string]any{"sources": json.RawMessage(sources), "model_response": json.RawMessage(model), "decision": json.RawMessage(decision), "verification": verification})
}

func (app *App) documentHandler(w http.ResponseWriter, r *http.Request) {
	s, _ := session(r)
	if s.Role != "client" {
		_ = writeJSON(w, 403, ErrorMsg{"FORBIDDEN", "Загрузка доступна клиенту"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 5<<20)
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		_ = writeJSON(w, 400, ErrorMsg{"INVALID_FILE", "Максимальный размер файла — 5 МБ"})
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	f, header, err := r.FormFile("file")
	if err != nil {
		_ = writeJSON(w, 400, ErrorMsg{"MISSING_FILE", "Выберите файл"})
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		app.endpointError(w, err)
		return
	}
	if len(data) == 0 {
		_ = writeJSON(w, 400, ErrorMsg{"EMPTY_FILE", "Файл пуст"})
		return
	}
	var id int64
	err = app.DB.QueryRow(r.Context(), `INSERT INTO demo_documents(company_id,name,content) VALUES($1,$2,$3) RETURNING document_id`, s.CompanyID, header.Filename, data).Scan(&id)
	if err != nil {
		app.endpointError(w, err)
		return
	}
	_ = writeJSON(w, 201, map[string]string{"document_id": strconv.FormatInt(id, 10), "name": header.Filename})
}
func (app *App) reviseHandler(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var input struct {
		Consent  *bool   `json:"bki_consent"`
		Document *string `json:"supplier_document_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil {
		_ = writeJSON(w, 400, ErrorMsg{"INVALID_JSON", "Некорректный запрос"})
		return
	}
	a, err := app.fullApplication(r.Context(), id)
	if err != nil {
		app.endpointError(w, err)
		return
	}
	if a.Status != "DRAFT" && a.Status != "NEED_INFO" {
		app.endpointError(w, errState)
		return
	}
	if input.Document != nil {
		var valid bool
		err = app.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM demo_documents WHERE document_id::text=$1 AND company_id=$2)`, *input.Document, a.CompanyID).Scan(&valid)
		if err != nil {
			app.endpointError(w, err)
			return
		}
		if !valid {
			_ = writeJSON(w, 400, ErrorMsg{"INVALID_DOCUMENT", "Документ не найден у этой компании"})
			return
		}
	}
	tag, err := app.DB.Exec(r.Context(), `UPDATE applications SET bki_consent=COALESCE($2,bki_consent),supplier_document_id=COALESCE($3,supplier_document_id) WHERE application_id=$1 AND status=$4`, id, input.Consent, input.Document, a.Status)
	if err != nil {
		app.endpointError(w, err)
		return
	}
	if tag.RowsAffected() != 1 {
		app.endpointError(w, errState)
		return
	}
	a, err = app.fullApplication(r.Context(), id)
	if err != nil {
		app.endpointError(w, err)
		return
	}
	_ = writeJSON(w, 200, a)
}
func (app *App) actionHandler(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	a, err := app.fullApplication(r.Context(), id)
	if err != nil {
		app.endpointError(w, err)
		return
	}
	action := r.PathValue("action")
	to, code := "", ""
	switch action {
	case "accept-offer":
		if a.Status == "APPROVED" || a.Status == "COUNTER_OFFER" {
			to, code = "SIGNING", "TERMS_ACCEPTED"
		}
	case "decline-offer":
		if a.Status == "COUNTER_OFFER" {
			to, code = "CLOSED", "OFFER_DECLINED"
		}
	case "sign":
		if a.Status == "SIGNING" {
			to, code = "ISSUED", "SIMULATED_SIGN_AND_ISSUE"
		}
	case "withdraw":
		if a.Status == "DRAFT" || a.Status == "SUBMITTED" || a.Status == "NEED_INFO" || a.Status == "MANUAL_REVIEW" {
			to, code = "CLOSED", "WITHDRAWN"
		}
	}
	if to == "" {
		app.endpointError(w, errState)
		return
	}
	if err = app.transition(r.Context(), id, a.Status, to, "CLIENT", code, nil); err != nil {
		app.endpointError(w, err)
		return
	}
	a, err = app.fullApplication(r.Context(), id)
	if err != nil {
		app.endpointError(w, err)
		return
	}
	_ = writeJSON(w, 200, a)
}
func (app *App) manualHandler(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var input struct {
		Decision   string `json:"decision"`
		Comment    string `json:"comment"`
		ReasonCode string `json:"reason_code"`
		Amount     int64  `json:"amount"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&input); err != nil {
		_ = writeJSON(w, 400, ErrorMsg{"INVALID_JSON", "Некорректный запрос"})
		return
	}
	if strings.TrimSpace(input.Comment) == "" {
		_ = writeJSON(w, 400, ErrorMsg{"COMMENT_REQUIRED", "Комментарий сотрудника обязателен"})
		return
	}
	if input.Decision != "APPROVED" && input.Decision != "COUNTER_OFFER" && input.Decision != "REJECTED" && input.Decision != "NEED_INFO" {
		_ = writeJSON(w, 400, ErrorMsg{"INVALID_DECISION", "Недопустимое решение"})
		return
	}
	if input.Decision == "REJECTED" && reasonMessages[input.ReasonCode] == "" {
		_ = writeJSON(w, 400, ErrorMsg{"REASON_REQUIRED", "Выберите причину отказа R01–R11"})
		return
	}
	tx, err := app.DB.Begin(r.Context())
	if err != nil {
		app.endpointError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var raw []byte
	var status string
	err = tx.QueryRow(r.Context(), `SELECT status,decision FROM applications WHERE application_id=$1 FOR UPDATE`, id).Scan(&status, &raw)
	if err != nil {
		app.endpointError(w, err)
		return
	}
	if status != "MANUAL_REVIEW" {
		app.endpointError(w, errState)
		return
	}
	var d CreditDecision
	if err = json.Unmarshal(raw, &d); err != nil {
		app.endpointError(w, err)
		return
	}
	if d.Offer == nil && d.ReviewTerms == nil && d.PD != nil && d.Grade != "" {
		// The row is locked; fullApplication derives missing legacy review
		// terms from this application's saved source snapshot.
		a, e := app.fullApplication(r.Context(), id)
		if e != nil {
			app.endpointError(w, e)
			return
		}
		if e = json.Unmarshal(a.Decision, &d); e != nil {
			app.endpointError(w, e)
			return
		}
	}
	recommendation := d.Recommendation
	originalAmount := int64(0)
	if d.Offer != nil {
		originalAmount = d.Offer.Amount
	}
	if input.Decision == "APPROVED" || input.Decision == "COUNTER_OFFER" {
		if d.Offer == nil && d.ReviewTerms != nil {
			copy := *d.ReviewTerms
			d.Offer = &copy
		}
		if d.Offer == nil || d.Limit.Amount <= 0 || input.Amount <= 0 || input.Amount > d.Limit.Amount {
			_ = writeJSON(w, 400, ErrorMsg{"LIMIT_EXCEEDED", "Сумма должна быть в пределах рассчитанного лимита; нужны полные данные"})
			return
		}
		var product string
		var requested int64
		err = tx.QueryRow(r.Context(), `SELECT product,amount FROM applications WHERE application_id=$1`, id).Scan(&product, &requested)
		if err != nil {
			app.endpointError(w, err)
			return
		}
		minimum := int64(300000)
		if product == "EQUIPMENT" {
			minimum = 500000
		}
		if input.Amount < minimum || (input.Decision == "APPROVED" && input.Amount != requested) || (input.Decision == "COUNTER_OFFER" && input.Amount > requested) {
			_ = writeJSON(w, 400, ErrorMsg{"INVALID_OFFER", "Проверьте сумму и тип решения"})
			return
		}
		d.Offer.Amount = input.Amount
		d.Offer.Payment = annuity(float64(input.Amount), d.Offer.Rate/100, d.Offer.Term)
	} else {
		d.Offer = nil
	}
	d.Decision = input.Decision
	d.Reasons = append(d.Reasons, Reason{"M01", input.Comment})
	if input.ReasonCode != "" {
		d.Reasons = append(d.Reasons, reason(input.ReasonCode))
	}
	payload, _ := json.Marshal(d)
	_, err = tx.Exec(r.Context(), `UPDATE applications SET status=$2,decision=$3::jsonb WHERE application_id=$1`, id, input.Decision, string(payload))
	if err != nil {
		app.endpointError(w, err)
		return
	}
	override := recommendation != input.Decision || ((input.Decision == "APPROVED" || input.Decision == "COUNTER_OFFER") && originalAmount != input.Amount)
	code := input.ReasonCode
	if code == "" {
		code = "M01"
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO application_events(application_id,from_status,to_status,actor,reason_code,comment,override) VALUES($1,'MANUAL_REVIEW',$2,'ANALYST',$3,$4,$5)`, id, input.Decision, code, input.Comment, override)
	if err != nil {
		app.endpointError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		app.endpointError(w, err)
		return
	}
	a, err := app.fullApplication(r.Context(), id)
	if err != nil {
		app.endpointError(w, err)
		return
	}
	_ = writeJSON(w, 200, a)
}
