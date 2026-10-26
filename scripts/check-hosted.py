"""Run the API smoke suite against a password-protected hosted demo."""
import base64
import os
from pathlib import Path
import runpy
import urllib.error
import urllib.request

base = os.getenv("API_URL", "http://127.0.0.1:8082")
password = os.environ["DEMO_ACCESS_PASSWORD"]
with urllib.request.urlopen(base + "/health", timeout=30) as response:
    assert response.status == 200
try:
    urllib.request.urlopen(base + "/", timeout=30)
except urllib.error.HTTPError as exc:
    assert exc.code == 401
else:
    raise AssertionError("Hosted demo is accessible without a password")

original_builder = urllib.request.build_opener


def protected_client(*handlers):
    client = original_builder(*handlers)
    encoded = base64.b64encode(("demo:" + password).encode()).decode()
    client.addheaders = [("Authorization", "Basic " + encoded)]
    return client


os.environ["API_URL"] = base
urllib.request.build_opener = protected_client
if __name__ == "__main__":
    runpy.run_path(str(Path(__file__).with_name("smoke.py")), run_name="__main__")
