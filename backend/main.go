package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type HealthResponse struct {
	Status string `json:"status"`
}

type Company struct {
	ID   int64  `json:"company_id"`
	INN  string `json:"inn"`
	Name string `json:"name"`
}

type App struct {
	DB     *pgxpool.Pool
	Logger *slog.Logger
}

type Application struct {
	ID        int64     `json:"application_id"`
	CompanyID int64     `json:"company_id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type ApplicationRequest struct {
	Inn                string  `json:"inn"`
	Amount             int64   `json:"amount"`
	Product            string  `json:"product"`
	TermMonths         int     `json:"term_months"`
	Purpose            string  `json:"purpose"`
	AnnualRevenue      int64   `json:"annual_revenue"`
	BKIConsent         *bool   `json:"bki_consent"`
	EquipmentCost      *int64  `json:"equipment_cost"`
	SupplierDocumentID *string `json:"supplier_document_id"`
}

type ErrorMsg struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func createApplication(
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID int64,
	req ApplicationRequest,
) (Application, error) {
	application := Application{}
	query := `
		INSERT INTO applications (company_id, product, amount, term_months, purpose, 
		declared_annual_revenue, bki_consent, equipment_cost,
		supplier_document_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING application_id, company_id, status, created_at;
	`
	if req.BKIConsent == nil {
		return Application{}, errors.New("bki_consent is null")
	}
	err := pool.QueryRow(ctx, query,
		companyID,
		req.Product,
		req.Amount,
		req.TermMonths,
		req.Purpose,
		req.AnnualRevenue,
		*req.BKIConsent,
		req.EquipmentCost,
		req.SupplierDocumentID,
	).Scan(&application.ID, &application.CompanyID,
		&application.Status, &application.CreatedAt)

	if err != nil {
		return Application{}, err
	}

	return application, nil
}

func getApplicationByID(
	ctx context.Context,
	pool *pgxpool.Pool,
	applicationID int64,
) (Application, error) {
	application := Application{}
	query := `
		SELECT application_id, company_id, status, created_at
		FROM applications
		WHERE application_id = $1
	
	`
	err := pool.QueryRow(ctx, query, applicationID).Scan(
		&application.ID,
		&application.CompanyID,
		&application.Status,
		&application.CreatedAt,
	)

	if err != nil {
		return Application{}, err
	}

	return application, nil
}

func getCompanyByINN(
	ctx context.Context,
	pool *pgxpool.Pool,
	inn string,
) (Company, error) {
	company := Company{}
	query := `
		SELECT company_id, inn, name
		FROM companies
		WHERE inn = $1	
	`
	err := pool.QueryRow(ctx, query, inn).Scan(
		&company.ID,
		&company.INN,
		&company.Name,
	)
	if err != nil {
		return Company{}, err
	}

	return company, nil
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	okResp := HealthResponse{Status: "ok"}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	w.WriteHeader(http.StatusOK)

	err := json.NewEncoder(w).Encode(okResp)
	if err != nil {
		logger.Error("не удалось записать ответ", "error", err)
	}
}

func validateEquipmentCost(product string, equipmentCost *int64) error {
	if product != "EQUIPMENT" {
		return nil
	}
	if equipmentCost == nil {
		return errors.New("equipment_cost is required")
	}
	if *equipmentCost <= 0 {
		return errors.New("equipment_cost must be a positive value")
	}
	return nil
}

func validateLoanTerms(product string, amount int64, termMonths int) error {
	switch product {
	case "EXPRESS":
		if amount < 300_000 || amount > 5_000_000 {
			return errors.New("invalid amount for EXPRESS")
		} else if termMonths < 6 || termMonths > 36 {
			return errors.New("invalid termMonths for EXPRESS")
		}
	case "EQUIPMENT":
		if amount < 500_000 || amount > 15_000_000 {
			return errors.New("invalid amount for EQUIPMENT")
		} else if termMonths < 12 || termMonths > 60 {
			return errors.New("invalid termMonths for EQUIPMENT")
		}
	default:
		return errors.New("unknown product")
	}
	return nil
}

func validateINN(inn string) error {
	if !(len(inn) == 10 || len(inn) == 12) {
		return errors.New("invalid length of inn")
	}
	for _, char := range inn {
		if char < '0' || char > '9' {
			return errors.New("invalid chars in inn")
		}
	}

	return nil
}

func writeJSON(w http.ResponseWriter, status int, data any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	return json.NewEncoder(w).Encode(data)
}

func (app *App) applicationHandler(w http.ResponseWriter, r *http.Request) {
	logger := app.Logger

	id := r.PathValue("id")
	applicationID, convErr := strconv.ParseInt(id, 10, 64)
	if convErr != nil || applicationID <= 0 {
		err := writeJSON(w, http.StatusBadRequest, ErrorMsg{
			Code:    "INVALID_APPLICATION_ID",
			Message: "Application ID must be a positive value",
		})
		if err != nil {
			logger.Error("не удалось записать тело ответа", "error", err)
		}
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	application, getApplicationErr := getApplicationByID(ctx, app.DB, applicationID)
	if errors.Is(getApplicationErr, pgx.ErrNoRows) {
		err := writeJSON(w, http.StatusNotFound, ErrorMsg{
			Code:    "APPLICATION_NOT_FOUND",
			Message: "Заявка не найдена",
		})
		if err != nil {
			logger.Error("не удалось записать тело ответа", "error", err)
		}
		return
	} else if getApplicationErr != nil {
		logger.Error("get application error", "error", getApplicationErr)
		err := writeJSON(w, http.StatusInternalServerError, ErrorMsg{
			Code:    "INTERNAL_ERROR",
			Message: "internal error",
		})
		if err != nil {
			logger.Error("не удалось записать тело ответа", "error", err)
		}
		return
	}

	err := writeJSON(w, http.StatusOK, application)
	if err != nil {
		logger.Error("не удалось записать тело ответа", "error", err)
	}
}

func (app *App) applicationsHandler(w http.ResponseWriter, r *http.Request) {
	logger := app.Logger

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var extra any

	appReq := ApplicationRequest{}
	err := decoder.Decode(&appReq)
	if err != nil {
		logger.Error("не удалось прочитать тело запроса", "error", err)
		err = writeJSON(w, http.StatusBadRequest, ErrorMsg{
			Code:    "INVALID_JSON",
			Message: "req body does not match with expected format",
		})
		if err != nil {
			logger.Error("не удалось записать тело ответа", "error", err)
		}
		return
	}

	err = decoder.Decode(&extra)
	if err != io.EOF {
		err = writeJSON(w, http.StatusBadRequest, ErrorMsg{
			Code:    "INVALID_JSON",
			Message: "req body does not match with expected format",
		})
		if err != nil {
			logger.Error("не удалось записать тело ответа", "error", err)
		}
		return
	}

	if validationErr := validateINN(appReq.Inn); validationErr != nil {
		err = writeJSON(w, http.StatusBadRequest, ErrorMsg{
			Code:    "INVALID_INN",
			Message: validationErr.Error(),
		})
		if err != nil {
			logger.Error("не удалось записать тело ответа", "error", err)
		}
		return
	}

	if validationErr := validateLoanTerms(appReq.Product, appReq.Amount, appReq.TermMonths); validationErr != nil {
		err = writeJSON(w, http.StatusBadRequest, ErrorMsg{
			Code:    "INVALID_LOAN_TERMS",
			Message: validationErr.Error(),
		})
		if err != nil {
			logger.Error("не удалось записать тело ответа", "error", err)
		}
		return
	}

	if strings.TrimSpace(appReq.Purpose) == "" {
		err = writeJSON(w, http.StatusBadRequest, ErrorMsg{
			Code:    "INVALID_PURPOSE",
			Message: "purpose must not be empty",
		})
		if err != nil {
			logger.Error("не удалось записать тело ответа", "error", err)
		}
		return
	}

	if appReq.AnnualRevenue <= 0 {
		err = writeJSON(w, http.StatusBadRequest, ErrorMsg{
			Code:    "INVALID_REVENUE",
			Message: "revenue must be a positive value",
		})
		if err != nil {
			logger.Error("не удалось записать тело ответа", "error", err)
		}
		return
	}

	if appReq.BKIConsent == nil {
		err = writeJSON(w, http.StatusBadRequest, ErrorMsg{
			Code:    "MISSING_BKI_CONSENT",
			Message: "bki_consent is required",
		})
		if err != nil {
			logger.Error("не удалось записать тело ответа", "error", err)
		}
		return
	}

	if validateErr := validateEquipmentCost(appReq.Product, appReq.EquipmentCost); validateErr != nil {
		err = writeJSON(w, http.StatusBadRequest, ErrorMsg{
			Code:    "INVALID_EQUIPMENT_COST",
			Message: validateErr.Error(),
		})
		if err != nil {
			logger.Error("не удалось записать тело ответа", "error", err)
		}
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	company, getCompanyErr := getCompanyByINN(ctx, app.DB, appReq.Inn)
	if errors.Is(getCompanyErr, pgx.ErrNoRows) {
		err := writeJSON(w, http.StatusNotFound, ErrorMsg{
			Code:    "COMPANY_NOT_FOUND",
			Message: "Компания не найдена",
		})
		if err != nil {
			logger.Error("не удалось записать тело ответа", "error", err)
		}
		return
	} else if getCompanyErr != nil {
		logger.Error("get company error", "error", getCompanyErr)
		err := writeJSON(w, http.StatusInternalServerError, ErrorMsg{
			Code:    "INTERNAL_ERROR",
			Message: "internal error",
		})
		if err != nil {
			logger.Error("не удалось записать тело ответа", "error", err)
		}
		return
	}

	application, createAppErr := createApplication(ctx, app.DB, company.ID, appReq)
	if createAppErr != nil {
		err := writeJSON(w, http.StatusInternalServerError, ErrorMsg{
			Code:    "INTERNAL_ERROR",
			Message: "internal error",
		})
		if err != nil {
			logger.Error("не удалось записать тело ответа", "error", err)
		}
		return
	}

	err = writeJSON(w, http.StatusCreated, application)
	if err != nil {
		logger.Error("не удалось записать тело ответа", "error", err)
	}

}

func connectDB(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}

	err = pool.Ping(ctx)
	if err != nil {
		pool.Close()
		return nil, err
	}

	return pool, nil
}

func (app *App) companyHandler(w http.ResponseWriter, r *http.Request) {
	logger := app.Logger
	inn := r.PathValue("inn")
	if validationErr := validateINN(inn); validationErr != nil {
		err := writeJSON(w, http.StatusBadRequest, ErrorMsg{
			Code:    "INVALID_INN",
			Message: validationErr.Error(),
		})
		if err != nil {
			logger.Error("не удалось записать тело ответа", "error", err)
		}
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	company, getCompanyErr := getCompanyByINN(ctx, app.DB, inn)
	if errors.Is(getCompanyErr, pgx.ErrNoRows) {
		err := writeJSON(w, http.StatusNotFound, ErrorMsg{
			Code:    "COMPANY_NOT_FOUND",
			Message: "Компания не найдена",
		})
		if err != nil {
			logger.Error("не удалось записать тело ответа", "error", err)
		}
		return
	} else if getCompanyErr != nil {
		logger.Error("get company error", "error", getCompanyErr)
		err := writeJSON(w, http.StatusInternalServerError, ErrorMsg{
			Code:    "INTERNAL_ERROR",
			Message: "internal error",
		})
		if err != nil {
			logger.Error("не удалось записать тело ответа", "error", err)
		}
		return
	}

	err := writeJSON(w, http.StatusOK, company)
	if err != nil {
		logger.Error("не удалось записать тело ответа", "error", err)
	}
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		logger.Error("DATABASE_URL is empty", "error", "DATABASE_URL is empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := connectDB(ctx, databaseURL)
	cancel()
	if err != nil {
		logger.Error("db connection error", "error", err)
		return
	}
	defer pool.Close()

	slog.Info("db is connected")

	app := App{
		DB:     pool,
		Logger: logger,
	}

	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	company, err := getCompanyByINN(ctx, pool, "7700000001")
	cancel()
	if errors.Is(err, pgx.ErrNoRows) {
		logger.Error("no row", "error", err)
		return
	} else if err != nil {
		logger.Error("get company error", "error", err)
		return
	} else {
		slog.Info("get company", "company_id", company.ID, "inn", company.INN, "name", company.Name)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/applications", app.applicationsHandler)
	mux.HandleFunc("GET /companies/{inn}", app.companyHandler)
	mux.HandleFunc("GET /applications/{id}", app.applicationHandler)

	slog.Info("server started", "address", "localhost:8080")

	err = http.ListenAndServe("localhost:8080", mux)
	if err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}

}
