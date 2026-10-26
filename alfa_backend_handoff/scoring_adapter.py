"""JSON boundary for the existing Alfa CatBoost; no HTTP server or credit policy."""
from pathlib import Path
import argparse
import hashlib
import json
import math
import sys

import numpy as np
import pandas as pd
from catboost import CatBoostClassifier
from jsonschema import Draft202012Validator

from feature_builder import FEATURE_COLUMNS, make_features, grade_from_pd


class InputValidationError(ValueError):
    def __init__(self, code, message, details=None):
        super().__init__(message)
        self.code = code
        self.details = details or []


def load_json(path):
    def reject_constant(value):
        raise InputValidationError('INVALID_JSON', f'{value} is not valid JSON; use null for missing values')
    with Path(path).open(encoding='utf-8') as stream:
        return json.load(stream, parse_constant=reject_constant)


class ScoringService:
    """Create once on startup; predict one request many times."""

    def __init__(self, root=None):
        self.root = Path(root) if root else Path(__file__).resolve().parent
        self.contract = load_json(self.root / 'models/feature_contract.json')
        if self.contract['feature_columns'] != FEATURE_COLUMNS:
            raise RuntimeError('Feature Builder and saved feature contract disagree')
        self.validator = Draft202012Validator(load_json(self.root / 'request.schema.json'))
        self.response_validator = Draft202012Validator(load_json(self.root / 'response.schema.json'))
        self.calibration = load_json(self.root / 'models/isotonic_calibration.json')
        self.mode = self.calibration['default_inference_mode']
        if self.mode not in ('raw', 'isotonic'):
            raise RuntimeError(f'Unsupported calibration mode: {self.mode}')
        model_path = self.root / 'models/catboost_pd.cbm'
        self.model = CatBoostClassifier()
        self.model.load_model(str(model_path))
        if self.model.feature_names_ != FEATURE_COLUMNS:
            raise RuntimeError('CatBoost feature order differs from the input contract')
        if list(self.model.classes_) != [0, 1]:
            raise RuntimeError('Unexpected target class order')
        self.model_id = 'sha256:' + hashlib.sha256(model_path.read_bytes()).hexdigest()

    def predict(self, request):
        def ensure_finite(value, path='$'):
            if isinstance(value, float) and not math.isfinite(value):
                raise InputValidationError('INVALID_JSON', f'{path}: use JSON null instead of NaN/Infinity')
            if isinstance(value, dict):
                for key, item in value.items(): ensure_finite(item, f'{path}.{key}')
            elif isinstance(value, list):
                for index, item in enumerate(value): ensure_finite(item, f'{path}[{index}]')
        ensure_finite(request)
        errors = sorted(self.validator.iter_errors(request), key=lambda e: str(list(e.path)))
        if errors:
            details = [{'path': '.'.join(map(str, e.path)) or '$', 'message': e.message} for e in errors[:12]]
            raise InputValidationError('INVALID_REQUEST', 'Request does not match the model input contract', details)
        # JSON null is converted to numerical NaN only inside Python. Objects remain keyed JSON on the wire.
        row = {name: request['features'][name] for name in FEATURE_COLUMNS}
        x = pd.DataFrame([row], columns=FEATURE_COLUMNS)
        for name in self.contract['categorical_features']:
            x[name] = x[name].astype(str)
        for name in self.contract['numeric_features']:
            x[name] = pd.to_numeric(x[name], errors='raise').astype(float)
        raw_pd = float(self.model.predict_proba(x)[:, 1][0])
        probability = float(np.interp(raw_pd, self.calibration['x_thresholds'], self.calibration['y_thresholds'])) if self.mode == 'isotonic' else raw_pd
        response = {
            'request_id': request['request_id'],
            'application_id': request['application_id'],
            'feature_schema_version': self.contract['schema_version'],
            'model_id': self.model_id,
            'pd': probability,
            'pd_grade': grade_from_pd(probability),
            'prediction_horizon_months': 12,
            'calibration_method': self.mode,
            'training_data_type': 'synthetic_counterfactuals',
        }
        self.response_validator.validate(response)
        return response

    def predict_safe(self, request):
        """Returns (payload, suggested HTTP status). The caller wires it to its own HTTP framework."""
        try:
            return self.predict(request), 200
        except InputValidationError as exc:
            rid = request.get('request_id') if isinstance(request, dict) else None
            aid = request.get('application_id') if isinstance(request, dict) else None
            return {'request_id': rid, 'application_id': aid,
                    'error': {'code': exc.code, 'message': str(exc), 'details': exc.details}}, 422

    def request_from_sources(self, sources, request_id):
        """Optional adapter: five saved source snapshots -> the identical notebook Feature Builder -> JSON request."""
        mapping = {'companies': 'company', 'applications': 'application', 'financial_profiles': 'financial_profile',
                   'bki_profiles': 'bki_profile', 'business_checks': 'business_checks'}
        missing = [key for key in mapping.values() if key not in sources]
        if missing:
            raise InputValidationError('SOURCE_DATA_INVALID', f'Missing source blocks: {missing}')
        app = sources['application']
        company_id = sources['company'].get('company_id')
        if not company_id or any(sources[key].get('company_id') != company_id for key in mapping.values()):
            raise InputValidationError('SOURCE_DATA_INVALID', 'Source company_id values disagree')
        if sources['company'].get('inn') != app.get('inn'):
            raise InputValidationError('SOURCE_DATA_INVALID', 'Application INN differs from company INN')
        tables = {name: pd.DataFrame([sources[key]]) for name, key in mapping.items()}
        try:
            x, _ = make_features(tables)
        except (KeyError, TypeError, ValueError) as exc:
            raise InputValidationError('SOURCE_DATA_INVALID', str(exc)) from exc
        features = {}
        for name in FEATURE_COLUMNS:
            value = x.iloc[0][name]
            if pd.isna(value):
                value = None
            elif isinstance(value, np.generic):
                value = value.item()
            allowed_types = self.validator.schema['properties']['features']['properties'][name]['type']
            if value is not None and 'integer' in allowed_types and float(value).is_integer():
                value = int(value)
            features[name] = value
        return {'request_id': request_id, 'application_id': app['application_id'],
                'feature_schema_version': self.contract['schema_version'], 'features': features}


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--input', default='input.example.json')
    parser.add_argument('--root', default=str(Path(__file__).resolve().parent))
    parser.add_argument('--sources', action='store_true', help='Input contains five source blocks instead of 32 features')
    args = parser.parse_args()
    service = ScoringService(args.root)
    request = load_json(args.input)
    if args.sources:
        request = service.request_from_sources(request, request_id='source-adapter-example')
    payload, status = service.predict_safe(request)
    print(json.dumps(payload, ensure_ascii=False, indent=2, allow_nan=False))
    sys.exit(0 if status == 200 else 2)
