"""Run native timer history and session E2E against a disposable backend."""

import argparse
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
from http.cookiejar import CookieJar
from urllib.request import HTTPCookieProcessor, Request, build_opener


def available_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def seed_timer(address):
    client = build_opener(HTTPCookieProcessor(CookieJar()))
    csrf = ""

    def call(path, data=None):
        request = Request(
            f"{address}/api/v1/{path}",
            data=json.dumps(data).encode() if data is not None else None,
            headers={"Content-Type": "application/json", "X-CSRF-Token": csrf},
        )
        with client.open(request, timeout=5) as response:
            return json.load(response)

    deadline = time.monotonic() + 30
    while True:
        try:
            call("health")
            break
        except OSError:
            if time.monotonic() >= deadline:
                raise
            time.sleep(0.25)

    test_password = "Timer-Test-Password-1"
    csrf = call("auth/init", {
        "username": "timer-admin", "password": test_password, "confirmPassword": test_password,
    })["csrfToken"]
    workflow = call("graph/workflow", {
        "name": "Native workflow",
        "config": {
            "nodes": [
                {"id": "start", "type": "start"},
                {"id": "shell", "type": "shell", "title": "Native timer shell",
                 "config": {"script": "echo native-timer-marker"}},
                {"id": "end", "type": "end"},
            ],
            "edges": [
                {"id": "start-shell", "sourceNodeId": "start", "targetNodeId": "shell"},
                {"id": "shell-end", "sourceNodeId": "shell", "targetNodeId": "end"},
            ],
        },
    })["workflow"]["id"]
    timer = call("schedule/create", {
        "name": "Native timer E2E", "kind": "graph", "cronExpr": "0 0 1 1 *",
        "graphWorkflowId": workflow, "enabled": False,
    })["schedule"]["id"]
    call(f"schedule/{timer}/run", {"clientRequestId": "native-timer-run"})
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        records = call(f"schedule/executions?scheduleId={timer}")["executions"]
        if records and records[0]["status"] == "completed":
            result = records[0]
            sessions = call(f"schedule/sessions?executionId={result['id']}")["sessions"]
            assert len(sessions) == 1 and sessions[0]["jobId"] == result["jobId"], sessions
            return
        time.sleep(0.25)
    raise RuntimeError(f"Timer failed to complete: {records!r}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--destination", default="platform=iOS Simulator,name=iPhone 17 Pro")
    parser.add_argument("--derived-data", default="/tmp/quartet-timer-ios")
    settings = parser.parse_args()
    repo = Path(__file__).resolve().parents[2]
    with tempfile.TemporaryDirectory(prefix="quartet-timer-ios-") as directory:
        sandbox = Path(directory)
        binary = sandbox / "quartet-web"
        certs = sandbox / "certs"
        certs.mkdir()
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/web"], cwd=repo, check=True)
        port = available_port()
        address = f"http://127.0.0.1:{port}"
        backend_env = {
            **os.environ, "LOCAL_MEMORY": str(sandbox / "memory"),
            "QUARTET_CERTS_DIR": str(certs), "QUARTET_LISTEN_ADDR": f"127.0.0.1:{port}",
        }
        with (sandbox / "backend.log").open("w+") as log:
            backend = subprocess.Popen([str(binary)], cwd=repo, env=backend_env, stdout=log, stderr=log)
            try:
                seed_timer(address)
                subprocess.run([
                    "xcodebuild", "-workspace", "ios/Quartet.xcworkspace", "-scheme", "Quartet",
                    "-configuration", "Debug", "-destination", settings.destination,
                    "-derivedDataPath", settings.derived_data, "-parallel-testing-enabled", "NO",
                    "-only-testing:QuartetUITests/QuartetUITests/testTimerExecutionAndSessionOnIsolatedBackend",
                    "CODE_SIGNING_ALLOWED=NO", "test",
                ], cwd=repo, env={**os.environ, "TEST_RUNNER_QUARTET_TIMER_E2E_URL": address}, check=True)
            except Exception:
                log.seek(0)
                print(log.read())
                raise
            finally:
                backend.terminate()
                try:
                    backend.wait(4)
                except subprocess.TimeoutExpired:
                    backend.kill()
                    backend.wait()


if __name__ == "__main__":
    main()
