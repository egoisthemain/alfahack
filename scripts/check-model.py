"""Checks the unchanged handoff model through our HTTP wrapper; stdlib only."""
import json
import os
from pathlib import Path
import urllib.request

root = Path(__file__).resolve().parents[1] / "alfa_backend_handoff"
base = os.getenv("MODEL_URL", "http://127.0.0.1:8000")
expected = json.loads((root / "output.example.json").read_text(encoding="utf-8"))
for endpoint, name in [("/predict", "input.example.json"), ("/predict-sources", "source_snapshots.example.json")]:
    req = urllib.request.Request(base + endpoint, data=(root / name).read_bytes(), headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as response:
        actual = json.load(response)
    assert abs(actual["pd"] - expected["pd"]) < 1e-10, actual
    assert actual["model_id"] == expected["model_id"]
    assert len(actual["features"]) == 32
    print(endpoint, "PASS", "PD=", actual["pd"], "grade=", actual["pd_grade"])
