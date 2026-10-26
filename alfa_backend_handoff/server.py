"""Local HTTP wrapper. The team's model and feature builder remain unchanged."""
import json
import os
from http.server import BaseHTTPRequestHandler, HTTPServer

from scoring_adapter import ScoringService, InputValidationError

scorer = ScoringService()


class Handler(BaseHTTPRequestHandler):
    def reply(self, status, payload):
        data = json.dumps(payload, ensure_ascii=False, allow_nan=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.path == "/health":
            return self.reply(200, {"status": "ok", "model_id": scorer.model_id})
        self.reply(404, {"error": {"code": "NOT_FOUND"}})

    def do_POST(self):
        if self.path not in ("/predict", "/predict-sources"):
            return self.reply(404, {"error": {"code": "NOT_FOUND"}})
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if not 0 < length <= 262144:
                return self.reply(413, {"error": {"code": "BODY_SIZE"}})
            def invalid_constant(value):
                raise ValueError("Non-finite JSON number")
            request = json.loads(self.rfile.read(length), parse_constant=invalid_constant)
            if self.path == "/predict-sources":
                request = scorer.request_from_sources(request, request_id="http-source-request")
            payload, status = scorer.predict_safe(request)
            if status == 200:
                # Preserve the exact features used, for the Go-side audit snapshot.
                payload["features"] = request["features"]
                import pandas as pd
                from catboost import Pool
                from feature_builder import FEATURE_COLUMNS
                x = pd.DataFrame([request["features"]], columns=FEATURE_COLUMNS)
                for name in scorer.contract["categorical_features"]:
                    x[name] = x[name].astype(str)
                for name in scorer.contract["numeric_features"]:
                    x[name] = pd.to_numeric(x[name], errors="raise").astype(float)
                values = scorer.model.get_feature_importance(
                    Pool(x, cat_features=scorer.contract["categorical_features"]), type="ShapValues"
                )[0][:-1]
                payload["top_factors"] = sorted(
                    [{"feature": name, "contribution": float(v)} for name, v in zip(FEATURE_COLUMNS, values) if v > 0],
                    key=lambda item: item["contribution"], reverse=True,
                )
                payload["explanation_scale"] = "raw_log_odds"
            self.reply(status, payload)
        except (InputValidationError, ValueError, TypeError, KeyError) as exc:
            self.reply(422, {"error": {"code": "SOURCE_DATA_INVALID", "message": str(exc)}})
        except Exception:
            import traceback
            traceback.print_exc()
            self.reply(500, {"error": {"code": "INFERENCE_FAILED"}})


if __name__ == "__main__":
    HTTPServer(("0.0.0.0", int(os.getenv("PORT", "8000"))), Handler).serve_forever()
