package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPDGradeBoundaries(t *testing.T) {
	for _, x := range []struct {
		pd    float64
		grade string
	}{{0, "A"}, {.03999, "A"}, {.04, "B"}, {.08, "C"}, {.15, "D"}, {.25, "E"}, {1, "E"}} {
		if g := pdGrade(x.pd); g != x.grade {
			t.Fatalf("PD %v: %s != %s", x.pd, g, x.grade)
		}
	}
}

func TestGradeEEquipmentAllowsBoundedHumanOverride(t *testing.T) {
	cost := int64(7_000_000)
	req := ApplicationRequest{Product: "EQUIPMENT", Amount: 2_000_000, TermMonths: 24, AnnualRevenue: 24_000_000, EquipmentCost: &cost}
	d := decisionFor(req, .35, "E", 100000, 24_000_000)
	if d.Decision != "MANUAL_REVIEW" || d.Recommendation != "REJECTED" || d.Offer != nil || d.ReviewTerms == nil || d.Limit.Amount < req.Amount {
		t.Fatalf("expected rejection recommendation with bounded manual terms: %#v", d)
	}
	if d.ReviewTerms.Amount > d.Limit.Amount || d.ReviewTerms.Rate > 30 {
		t.Fatal("invalid review terms")
	}
	d = decisionFor(req, .35, "E", 2_000_000, 24_000_000)
	if d.Limit.Amount != 0 || d.ReviewTerms != nil {
		t.Fatal("human override must respect exhausted payment capacity")
	}
	req.Product = "EXPRESS"
	d = decisionFor(req, .35, "E", 0, 24_000_000)
	if d.Decision != "REJECTED" || d.Offer != nil {
		t.Fatal("grade E must not be auto-approved")
	}
}
func TestRatesAndManualLimit(t *testing.T) {
	req := ApplicationRequest{Product: "EXPRESS", Amount: 2_000_000, TermMonths: 24, AnnualRevenue: 24_000_000}
	for _, x := range []struct{ pd, rate float64 }{{.02, 17}, {.06, 19.5}, {.1, 22}, {.2, 28}} {
		d := decisionFor(req, x.pd, pdGrade(x.pd), 100000, 24_000_000)
		if d.Offer == nil || d.Offer.Rate != x.rate {
			t.Fatalf("PD %v: %#v", x.pd, d)
		}
	}
	req.Amount = 4_000_000
	d := decisionFor(req, .02, "A", 0, 24_000_000)
	if d.Decision != "MANUAL_REVIEW" || d.Recommendation != "APPROVED" {
		t.Fatalf("large offer must require review: %#v", d)
	}
	req.AnnualRevenue = 100000
	d = decisionFor(req, .02, "A", 100000, 100000)
	if d.Decision != "REJECTED" {
		t.Fatal("cannot approve a nonpositive payment limit")
	}
}
func TestTwelveFixturePoints(t *testing.T) {
	var fixtures []map[string]any
	if err := json.Unmarshal(demoFixtures, &fixtures); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	want := map[int]int{0: 95, 1: 70, 2: 70, 3: 45, 4: 30, 5: 0, 10: 95, 11: 45}
	for i, w := range want {
		s := fixtures[i]
		cm := block(s, "company")
		reg, _ := time.Parse("2006-01-02", text(cm, "registration_date"))
		c := Company{LegalForm: text(cm, "legal_form"), RegistrationDate: reg, Status: text(cm, "status"), OKVED: text(cm, "okved")}
		raw, _ := json.Marshal(s["demo_request"])
		var req ApplicationRequest
		_ = json.Unmarshal(raw, &req)
		got, _ := pointsScore(c, req, block(s, "bki_profile"), block(s, "business_checks"), at)
		if got != w {
			t.Errorf("company %d: points %d, want %d", i+1, got, w)
		}
	}
}
func TestRegistryStopsAndCalendarAge(t *testing.T) {
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	c := Company{Status: "ACTIVE", RegistrationDate: at.AddDate(-3, 0, 0), OKVED: "47.11"}
	if len(registryStops(c, map[string]any{}, map[string]any{}, at)) != 0 {
		t.Fatal("eligible company blocked")
	}
	c.Status = "LIQUIDATING"
	if registryStops(c, nil, nil, at)[0].Code != "SF01" {
		t.Fatal("missing SF01")
	}
	for _, ok := range []string{"92.01", "11.01", "11.05.1", "12.00"} {
		if !prohibited(ok) {
			t.Errorf("not prohibited: %s", ok)
		}
	}
	if prohibited("11.06") || prohibited("47.11") {
		t.Fatal("allowed industry blocked")
	}
	if ageMonths(time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC), at) != 2 {
		t.Fatal("must use full months")
	}
}
