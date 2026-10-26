"""One small demo instance: supervise the local model and Go API together."""
import os
import signal
import subprocess
import sys
import time
import urllib.request

children = []


def stop(*_):
    for child in reversed(children):
        if child.poll() is None:
            child.terminate()
    for child in reversed(children):
        try:
            child.wait(timeout=8)
        except subprocess.TimeoutExpired:
            child.kill()
            child.wait()


def shutdown(*_):
    stop()
    raise SystemExit(0)


signal.signal(signal.SIGTERM, shutdown)
signal.signal(signal.SIGINT, shutdown)
try:
    if not os.environ.get("DATABASE_URL"):
        raise RuntimeError("DATABASE_URL is required")
    if not os.environ.get("DEMO_ACCESS_PASSWORD"):
        raise RuntimeError("DEMO_ACCESS_PASSWORD is required for the hosted demo")
    model_env = dict(os.environ, PORT="8000")
    children.append(subprocess.Popen([sys.executable, "server.py"], cwd="/app/model", env=model_env))
    for _ in range(120):
        if children[0].poll() is not None:
            raise RuntimeError("Model stopped during startup")
        try:
            with urllib.request.urlopen("http://127.0.0.1:8000/health", timeout=1) as response:
                if response.status == 200:
                    break
        except OSError:
            time.sleep(1)
    else:
        raise RuntimeError("Model readiness timeout")
    api_env = dict(os.environ, HTTP_ADDR="0.0.0.0:" + os.getenv("PORT", "10000"))
    children.append(subprocess.Popen(["/app/api"], env=api_env))
    while all(child.poll() is None for child in children):
        time.sleep(1)
    raise RuntimeError("A demo service exited; stopping the instance")
finally:
    stop()
