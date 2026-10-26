package main

import (
	"math"
	"sort"
	"strings"
	"time"
)

type Reason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type Offer struct {
	Amount  int64   `json:"amount"`
	Term    int     `json:"term_months"`
	Rate    float64 `json:"rate"`
	Payment float64 `json:"monthly_payment"`
}
type CreditLimit struct {
	Amount  int64  `json:"amount"`
	Binding string `json:"binding"`
}
type CreditDecision struct {
	Decision       string      `json:"decision"`
	Grade          string      `json:"grade,omitempty"`
	PD             *float64    `json:"pd,omitempty"`
	Mode           string      `json:"scoring_mode"`
	ModelID        string      `json:"model_id,omitempty"`
	Offer          *Offer      `json:"offer,omitempty"`
	ReviewTerms    *Offer      `json:"review_terms,omitempty"`
	Limit          CreditLimit `json:"limit"`
	Reasons        []Reason    `json:"reasons"`
	Recommendation string      `json:"recommendation,omitempty"`
	Triggers       []string    `json:"manual_review_triggers"`
	PolicyVersion  string      `json:"policy_version"`
	Score          *int        `json:"score,omitempty"`
}

func num(m map[string]any, key string) float64 { v, _ := m[key].(float64); return v }
func flag(m map[string]any, key string) bool   { v, _ := m[key].(bool); return v }
func text(m map[string]any, key string) string { v, _ := m[key].(string); return v }
func block(m map[string]any, key string) map[string]any {
	v, _ := m[key].(map[string]any)
	if v == nil {
		return map[string]any{}
	}
	return v
}
func ageMonths(reg, at time.Time) int {
	y, m, d := reg.Date()
	ay, am, ad := at.Date()
	n := (ay-y)*12 + int(am-m)
	if ad < d {
		n--
	}
	return n
}
func prohibited(okved string) bool {
	if okved == "92" || okved == "12" || strings.HasPrefix(okved, "92.") || strings.HasPrefix(okved, "12.") {
		return true
	}
	for _, p := range []string{"11.01", "11.02", "11.03", "11.04", "11.05"} {
		if okved == p || strings.HasPrefix(okved, p+".") {
			return true
		}
	}
	return false
}
func registryStops(c Company, bki, pb map[string]any, at time.Time) []Reason {
	r := []Reason{}
	if c.Status == "LIQUIDATING" {
		r = append(r, Reason{"SF01", "Компания находится в процессе ликвидации"})
	}
	if c.Status == "BANKRUPT" || flag(bki, "bankruptcy") {
		r = append(r, Reason{"SF02", "В отношении компании ведётся процедура банкротства"})
	}
	if ageMonths(c.RegistrationDate, at) < 3 {
		r = append(r, Reason{"SF03", "Бизнес зарегистрирован менее 3 месяцев назад"})
	}
	if num(bki, "current_overdue_days") > 30 {
		r = append(r, Reason{"SF04", "Текущая просрочка по кредиту больше 30 дней"})
	}
	if flag(pb, "director_disqualified") {
		r = append(r, Reason{"SF05", "Руководитель компании дисквалифицирован"})
	}
	if prohibited(c.OKVED) {
		r = append(r, Reason{"SF06", "Банк не кредитует этот вид деятельности"})
	}
	return r
}
func annuity(amount, rate float64, n int) float64 {
	if rate == 0 {
		return amount / float64(n)
	}
	r := rate / 12
	return amount * r / (1 - math.Pow(1+r, -float64(n)))
}
func pdGrade(pd float64) string {
	if pd < .04 {
		return "A"
	}
	if pd < .08 {
		return "B"
	}
	if pd < .15 {
		return "C"
	}
	if pd < .25 {
		return "D"
	}
	return "E"
}
func risky(ok string) bool {
	for _, p := range []string{"41", "42", "43", "47.25", "56"} {
		if ok == p || strings.HasPrefix(ok, p+".") {
			return true
		}
	}
	return false
}

var reasonMessages = map[string]string{
	"R01": "Небольшой срок работы бизнеса", "R02": "Высокая долговая нагрузка", "R03": "Просрочки в кредитной истории за последний год", "R04": "Низкий кредитный рейтинг", "R05": "Сумма велика относительно выручки", "R06": "Отрасль повышенного риска", "R07": "Массовый адрес регистрации", "R08": "Долг у судебных приставов", "R09": "Отсутствует отчётность за год", "R10": "Невысокая выручка бизнеса", "R11": "Сумма больше 90% стоимости оборудования",
}

func reason(code string) Reason { return Reason{code, reasonMessages[code]} }

func modelReasons(model map[string]any) []Reason {
	aliases := map[string]string{"age_months": "R01", "business_age_months": "R01", "dti": "R02", "payment_to_revenue": "R02", "existing_monthly_payment_rub": "R02", "max_overdue_days_12m": "R03", "rating": "R04", "amount_ratio": "R05", "loan_amount_to_revenue": "R05", "amount": "R05", "okved": "R06", "industry_group": "R06", "mass_address": "R07", "bailiff_debt_rub": "R08", "enforcement_debt_rub": "R08", "no_reporting_12m": "R09", "annual_revenue": "R10", "verified_annual_revenue": "R10", "declared_annual_revenue": "R10"}
	items, _ := model["top_factors"].([]any)
	out := []Reason{}
	seen := map[string]bool{}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok || num(m, "contribution") <= 0 {
			continue
		}
		code := aliases[text(m, "feature")]
		if code != "" && !seen[code] {
			out = append(out, reason(code))
			seen[code] = true
		}
		if len(out) == 3 {
			break
		}
	}
	return out
}
func pointsScore(c Company, req ApplicationRequest, bki, pb map[string]any, at time.Time) (int, []Reason) {
	a := ageMonths(c.RegistrationDate, at)
	f := []int{0, 0, 0, 0, 0, 10}
	max := []int{20, 25, 20, 15, 10, 10}
	if a >= 60 {
		f[0] = 20
	} else if a >= 24 {
		f[0] = 15
	} else if a >= 12 {
		f[0] = 10
	} else if a >= 6 {
		f[0] = 5
	}
	dti := (num(bki, "existing_monthly_payment_rub") + annuity(float64(req.Amount), .25, req.TermMonths)) / (float64(req.AnnualRevenue) / 12)
	if dti <= .2 {
		f[1] = 25
	} else if dti <= .35 {
		f[1] = 15
	} else if dti <= .5 {
		f[1] = 5
	}
	overdue := num(bki, "max_overdue_days_12m")
	if overdue == 0 {
		f[2] = 20
	} else if overdue <= 30 {
		f[2] = 10
	}
	rating := num(bki, "rating")
	if rating >= 750 {
		f[3] = 15
	} else if rating >= 650 {
		f[3] = 10
	} else if rating >= 500 {
		f[3] = 5
	}
	ratio := float64(req.Amount) / float64(req.AnnualRevenue)
	if ratio <= .15 {
		f[4] = 10
	} else if ratio <= .3 {
		f[4] = 5
	}
	if risky(c.OKVED) {
		f[5] = 0
	}
	type loss struct {
		code  string
		value int
	}
	losses := []loss{}
	score := 0
	codes := []string{"R01", "R02", "R03", "R04", "R05", "R06"}
	for i, v := range f {
		score += v
		if max[i] > v {
			losses = append(losses, loss{codes[i], max[i] - v})
		}
	}
	for i, on := range []bool{flag(pb, "mass_address"), num(pb, "enforcement_debt_rub") > 1000, flag(pb, "no_reporting_12m")} {
		if on {
			score -= 10
			losses = append(losses, loss{[]string{"R07", "R08", "R09"}[i], 10})
		}
	}
	sort.SliceStable(losses, func(i, j int) bool { return losses[i].value > losses[j].value })
	rs := []Reason{}
	for i, v := range losses {
		if i == 3 {
			break
		}
		rs = append(rs, reason(v.code))
	}
	if score < 0 {
		score = 0
	}
	return score, rs
}
func decisionFor(req ApplicationRequest, pd float64, grade string, existing, annualRevenue float64) CreditDecision {
	d := CreditDecision{Decision: "REJECTED", Grade: grade, PD: &pd, Mode: "model", Reasons: []Reason{}, Triggers: []string{}, PolicyVersion: "2026-10-22.1"}
	if grade == "E" {
		if req.Product == "EQUIPMENT" {
			d.Recommendation = "REJECTED"
			d.Decision = "MANUAL_REVIEW"
			d.Triggers = append(d.Triggers, "MR01")
		}
		return d
	}
	base, lgd, cap, revCap, minAmount := .16, .6, 5_000_000., .4, 300_000.
	if req.Product == "EQUIPMENT" {
		base, lgd, cap, revCap, minAmount = .14, .4, 15_000_000, 1, 500_000
	}
	rate := math.Min(.30, math.Floor((base+pd*lgd)/.005+.5)*.005)
	limits := []struct {
		name string
		v    float64
	}{{"PRODUCT", cap}, {"REVENUE", revCap * annualRevenue}, {"DTI", math.Max(0, .5*annualRevenue/12-existing) / annuity(1, rate, req.TermMonths)}}
	if req.Product == "EQUIPMENT" && req.EquipmentCost != nil {
		limits = append(limits, struct {
			name string
			v    float64
		}{"COLLATERAL", .9 * float64(*req.EquipmentCost)})
	}
	binding := limits[0]
	for _, l := range limits[1:] {
		if l.v < binding.v {
			binding = l
		}
	}
	limit := int64(math.Floor(binding.v/10000) * 10000)
	d.Limit = CreditLimit{limit, binding.name}
	amount := math.Min(float64(req.Amount), float64(limit))
	result := "APPROVED"
	if int64(amount) < req.Amount {
		result = "COUNTER_OFFER"
	}
	if grade == "C" {
		amount = math.Min(.7*float64(req.Amount), float64(limit))
		result = "COUNTER_OFFER"
	}
	if grade == "D" {
		amount = math.Min(.5*float64(req.Amount), float64(limit))
		result = "COUNTER_OFFER"
		if req.Product == "EQUIPMENT" {
			result = "REJECTED"
		}
	}
	if amount < minAmount {
		result = "REJECTED"
	}
	if result != "REJECTED" {
		d.Offer = &Offer{int64(math.Floor(amount)), req.TermMonths, math.Round(rate*10000) / 100, math.Round(annuity(math.Floor(amount), rate, req.TermMonths)*100) / 100}
	}
	if result == "COUNTER_OFFER" {
		if code := map[string]string{"REVENUE": "R05", "DTI": "R02", "COLLATERAL": "R11"}[binding.name]; code != "" {
			d.Reasons = append(d.Reasons, reason(code))
		}
	}
	if req.Product == "EQUIPMENT" {
		d.Triggers = append(d.Triggers, "MR01")
	}
	if grade == "D" {
		d.Triggers = append(d.Triggers, "MR02")
	}
	if d.Offer != nil && d.Offer.Amount > 3_000_000 {
		d.Triggers = append(d.Triggers, "MR03")
	}
	d.Decision = result
	if len(d.Triggers) > 0 {
		d.Decision = "MANUAL_REVIEW"
		d.Recommendation = result
		if grade == "D" && req.Product == "EQUIPMENT" && float64(limit) >= minAmount {
			candidate := math.Min(float64(req.Amount), float64(limit))
			d.ReviewTerms = &Offer{int64(candidate), req.TermMonths, math.Round(rate*10000) / 100, math.Round(annuity(candidate, rate, req.TermMonths)*100) / 100}
		}
	}
	return d
}
