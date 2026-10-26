"""End-to-end checks against the running demo; creates synthetic applications."""
import http.cookiejar
import json
import os
import urllib.error
import urllib.request

BASE = os.getenv("API_URL", "http://127.0.0.1:8081")


def client():
    return urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))


def call(c, method, path, body=None, expected=200):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(BASE + path, data=data, method=method, headers={"Content-Type": "application/json"})
    try:
        response = c.open(req, timeout=40)
    except urllib.error.HTTPError as exc:
        response = exc
    raw = response.read()
    try:
        out = json.loads(raw)
    except ValueError:
        raise AssertionError((path, response.code, raw))
    assert response.code == expected, (method, path, response.code, out)
    return out


def login(c, inn, role="client"):
    return call(c, "POST", "/demo/login", {"role": role, "inn": inn})


def draft(c, inn, product="EXPRESS", consent=True):
    return call(c, "POST", "/applications", {
        "inn": inn, "product": product, "amount": 2000000 if product == "EXPRESS" else 6000000,
        "term_months": 24 if product == "EXPRESS" else 60, "purpose": "Synthetic smoke test",
        "annual_revenue": 24000000 if product == "EXPRESS" else 36000000,
        "bki_consent": consent, "equipment_cost": None if product == "EXPRESS" else 7000000,
        "supplier_document_id": None,
    }, expected=201)["application_id"]


def submit(c, aid):
    call(c, "POST", f"/applications/{aid}/submit")
    call(c, "POST", f"/applications/{aid}/submit", expected=409)
    events = call(c, "GET", f"/applications/{aid}/history")
    assert len(events) == 1
    return call(c, "POST", f"/applications/{aid}/process")


guest = client()
call(guest, "GET", "/applications", expected=401)
c1 = client()
login(c1, "7700000001")
aid = draft(c1, "7700000001")
a = submit(c1, aid)
assert a["decision"]["scoring_mode"] == "model", a
assert a["decision"]["model_id"].startswith("sha256:"), a
assert 0 <= a["decision"]["pd"] <= 1
print("PASS real CatBoost through Go, ID", aid, "status", a["status"], "PD", a["decision"]["pd"])
call(c1, "POST", f"/applications/{aid}/process", expected=409)
call(c1, "POST", f"/applications/{aid}/decision", {"decision": "APPROVED", "comment": "forbidden"}, expected=403)
if a["status"] in ("APPROVED", "COUNTER_OFFER"):
    call(c1, "POST", f"/applications/{aid}/accept-offer")
    issued = call(c1, "POST", f"/applications/{aid}/sign")
    assert issued["status"] == "ISSUED"
    print("PASS client acceptance, simulated signing and issue")

c7 = client()
login(c7, "7700000007")
bad = draft(c7, "7700000007")
a = submit(c7, bad)
assert a["status"] == "REJECTED" and a["decision"]["reasons"][0]["code"] == "SF01", a
assert a["decision"]["scoring_mode"] == "not_scored"
call(c1, "GET", f"/applications/{bad}/detail", expected=403)
print("PASS stop factor before inference and cross-company access")

missing = draft(c1, "7700000001", consent=False)
a = submit(c1, missing)
assert a["status"] == "NEED_INFO"
call(c1, "PATCH", f"/applications/{missing}", {"bki_consent": True})
a = call(c1, "POST", f"/applications/{missing}/process")
assert a["decision"]["scoring_mode"] == "model"
print("PASS consent and resubmission from NEED_INFO")

c11 = client()
login(c11, "7700000011")
eq = draft(c11, "7700000011", "EQUIPMENT")
a = submit(c11, eq)
assert a["status"] == "NEED_INFO" and a["decision"]["reasons"][0]["code"] == "N01"
boundary = "AlfaSmokeBoundary"
content = (f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"synthetic-invoice.txt\"\r\nContent-Type: text/plain\r\n\r\nSYNTHETIC invoice, no actual payment.\r\n--{boundary}--\r\n").encode()
req = urllib.request.Request(BASE + "/documents", data=content, headers={"Content-Type": "multipart/form-data; boundary=" + boundary})
with c11.open(req, timeout=10) as res:
    doc = json.load(res)["document_id"]
call(c11, "PATCH", f"/applications/{eq}", {"supplier_document_id": doc})
a = call(c11, "POST", f"/applications/{eq}/process")
assert a["status"] == "MANUAL_REVIEW" and "MR01" in a["decision"]["manual_review_triggers"], a
print("PASS document upload and equipment review, ID", eq)

analyst = client()
login(analyst, "7700000001", "analyst")
queue = call(analyst, "GET", "/applications")
assert any(x["application_id"] == eq for x in queue)
audit = call(analyst, "GET", f"/applications/{eq}/audit")
assert audit["model_response"]["model_id"].startswith("sha256:")
call(c11, "GET", f"/applications/{eq}/audit", expected=403)
if (a["decision"].get("offer") or a["decision"].get("review_terms")) and a["decision"]["limit"]["amount"] >= 6000000:
    call(analyst, "POST", f"/applications/{eq}/decision", {"decision": "APPROVED", "amount": 6000000, "comment": "Synthetic invoice checked"})
    call(c11, "POST", f"/applications/{eq}/accept-offer")
    a = call(c11, "POST", f"/applications/{eq}/sign")
    assert a["status"] == "ISSUED"
    print("PASS analyst decision, client acceptance and simulated issue")
else:
    call(analyst, "POST", f"/applications/{eq}/decision", {"decision": "NEED_INFO", "comment": "Additional synthetic data requested"})
    print("PASS analyst requests more information; actual model did not propose requested conditions")
call(c1, "GET", "/applications/0", expected=400)
print("ALL SMOKE CHECKS PASSED")
