package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Deliberately a test-account selector, not production authentication.
type demoSession struct {
	Role      string
	CompanyID int64
	Expires   time.Time
}

var sessions sync.Map

func session(r *http.Request) (demoSession, bool) {
	c, err := r.Cookie("alfahack_session")
	if err != nil {
		return demoSession{}, false
	}
	v, ok := sessions.Load(c.Value)
	if !ok {
		return demoSession{}, false
	}
	s := v.(demoSession)
	if time.Now().After(s.Expires) {
		sessions.Delete(c.Value)
		return demoSession{}, false
	}
	return s, true
}
func (app *App) loginHandler(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Role string `json:"role"`
		INN  string `json:"inn"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil {
		_ = writeJSON(w, 400, ErrorMsg{"INVALID_JSON", "Некорректный запрос"})
		return
	}
	s := demoSession{Role: input.Role, Expires: time.Now().Add(12 * time.Hour)}
	if s.Role == "client" {
		c, err := getCompanyByINN(r.Context(), app.DB, input.INN)
		if err != nil {
			app.endpointError(w, err)
			return
		}
		s.CompanyID = c.ID
	} else if s.Role != "analyst" {
		_ = writeJSON(w, 400, ErrorMsg{"INVALID_ROLE", "Неизвестная роль"})
		return
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		app.endpointError(w, err)
		return
	}
	key := hex.EncodeToString(token)
	sessions.Store(key, s)
	http.SetCookie(w, &http.Cookie{Name: "alfahack_session", Value: key, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	_ = writeJSON(w, 200, map[string]any{"role": s.Role, "company_id": s.CompanyID})
}
func (app *App) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Consume small rejected request bodies before the connection closes.
		// Otherwise a Windows client can observe a TCP reset instead of the 403.
		if r.Body != nil && r.ContentLength >= 0 && r.ContentLength <= 64<<10 {
			defer func() { _, _ = io.Copy(io.Discard, r.Body) }()
		}
		p := r.URL.Path
		if p == "/" || strings.HasPrefix(p, "/assets/") || p == "/health" || p == "/companies" || strings.HasPrefix(p, "/companies/") || p == "/demo/login" {
			next.ServeHTTP(w, r)
			return
		}
		s, ok := session(r)
		if !ok {
			_ = writeJSON(w, 401, ErrorMsg{"LOGIN_REQUIRED", "Выберите тестовую учётную запись"})
			return
		}
		parts := strings.Split(strings.Trim(p, "/"), "/")
		if len(parts) >= 2 && parts[0] == "applications" {
			id, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil || id <= 0 {
				_ = writeJSON(w, 400, ErrorMsg{"INVALID_APPLICATION_ID", "ID должен быть положительным числом"})
				return
			}
			var companyID int64
			err = app.DB.QueryRow(r.Context(), `SELECT company_id FROM applications WHERE application_id=$1`, id).Scan(&companyID)
			if err != nil {
				app.endpointError(w, err)
				return
			}
			if s.Role == "client" && companyID != s.CompanyID {
				_ = writeJSON(w, 403, ErrorMsg{"FORBIDDEN", "Нет доступа к этой заявке"})
				return
			}
			isDecision := len(parts) == 3 && parts[2] == "decision"
			if r.Method != "GET" && ((isDecision && s.Role != "analyst") || (!isDecision && s.Role != "client")) {
				_ = writeJSON(w, 403, ErrorMsg{"FORBIDDEN", "Действие недоступно для вашей роли"})
				return
			}
		}
		if p == "/applications" && r.Method == "POST" && s.Role != "client" {
			_ = writeJSON(w, 403, ErrorMsg{"FORBIDDEN", "Заявки создаёт клиент"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
