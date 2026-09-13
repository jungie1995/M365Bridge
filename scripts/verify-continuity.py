"""Opt-in live regression tests. Uses disposable fixtures, never a user's project.

Requires a running bridge and an API key in M365BRIDGE_API_KEY (or the Windows
user environment). Native tests additionally need OpenCode/Codex and Python.
Reports contain assertions and protocol metadata, never authentication values.
"""
from __future__ import annotations

import argparse
import base64
import hashlib
import io
import json
import os
from pathlib import Path
import queue
import re
import subprocess
import sys
import threading
import time
import secrets
import urllib.error
import urllib.parse
import urllib.request


def key_from_environment():
    key = os.environ.get("M365BRIDGE_API_KEY", "")
    if not key and os.name == "nt":
        import winreg
        try:
            with winreg.OpenKey(winreg.HKEY_CURRENT_USER, "Environment") as handle:
                key = str(winreg.QueryValueEx(handle, "M365BRIDGE_API_KEY")[0])
        except OSError:
            pass
    return key


def http(url, body=None, key="", timeout=180, extra_headers=None):
    headers = {"Content-Type": "application/json"}
    headers.update(extra_headers or {})
    if key:
        headers["Authorization"] = "Bearer " + key
    request = urllib.request.Request(url, data=None if body is None else json.dumps(body).encode(), headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            raw = response.read().decode()
            return json.loads(raw) if raw else None
    except urllib.error.HTTPError as error:
        # Do not echo arbitrary response bodies/headers from a credentialed request.
        raise RuntimeError(f"HTTP {error.code} from {urllib.parse.urlsplit(url).path}") from None


def api_test(base, key, model, protocol, stream):
    schema = {"type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"]}
    prompt = "Read employee.py using read_file. This is a real tool request: after the intervening patch in the supplied history, a fresh read is required to verify the fix. Return the next read_file call, not an announcement or final answer."
    calls = [("r1", "read_file", {"path": "employee.py"}, "old contents"),
             ("r2", "read_file", {"path": "employee.py"}, "old contents"),
             ("r3", "read_file", {"path": "employee.py"}, "old contents"),
             ("edit", "apply_patch", {"patch": "correct employee validation"}, "The file was modified. Verification has not run.")]
    messages = [{"role": "user", "content": prompt}]
    if protocol == "responses":
        for call_id, name, args, output in calls:
            messages += [{"type": "function_call", "call_id": call_id, "name": name, "arguments": json.dumps(args)},
                         {"type": "function_call_output", "call_id": call_id, "output": output}]
        body = {"model": model, "input": messages, "tools": [{"type": "function", "name": "read_file", "parameters": schema}], "stream": stream}
        endpoint = "/responses"
    elif protocol == "anthropic":
        for call_id, name, args, output in calls:
            messages += [{"role": "assistant", "content": [{"type": "tool_use", "id": call_id, "name": name, "input": args}]},
                         {"role": "user", "content": [{"type": "tool_result", "tool_use_id": call_id, "content": output}]}]
        body = {"model": model, "messages": messages, "tools": [{"name": "read_file", "input_schema": schema}], "max_tokens": 2048, "stream": stream}
        endpoint = "/messages"
    else:
        for call_id, name, args, output in calls:
            messages += [{"role": "assistant", "tool_calls": [{"id": call_id, "type": "function", "function": {"name": name, "arguments": json.dumps(args)}}]},
                         {"role": "tool", "tool_call_id": call_id, "content": output}]
        body = {"model": model, "messages": messages, "tools": [{"type": "function", "function": {"name": "read_file", "parameters": schema}}], "stream": stream}
        endpoint = "/chat/completions"
    request = urllib.request.Request(base + endpoint, data=json.dumps(body).encode(), headers={"Content-Type": "application/json", "Authorization": "Bearer " + key})
    with urllib.request.urlopen(request, timeout=180) as response:
        raw = response.read().decode()
    if stream:
        events = [json.loads(line[6:]) for line in raw.splitlines() if line.startswith("data: ") and line[6:] != "[DONE]"]
        assert not any(event.get("error") or event.get("type") == "response.failed" for event in events), "stream reported failure"
        if protocol == "responses":
            outputs = [e.get("item", {}) for e in events if e.get("type") == "response.output_item.done"]
            found = any(item.get("type") == "function_call" and item.get("name") == "read_file" for item in outputs)
        elif protocol == "anthropic":
            found = any(e.get("content_block", {}).get("name") == "read_file" for e in events)
        else:
            found = any(call.get("function", {}).get("name") == "read_file" for e in events for choice in e.get("choices", []) for call in choice.get("delta", {}).get("tool_calls", []))
    else:
        data = json.loads(raw)
        if protocol == "responses":
            found = any(item.get("type") == "function_call" and item.get("name") == "read_file" for item in data.get("output", []))
        elif protocol == "anthropic":
            found = any(item.get("type") == "tool_use" and item.get("name") == "read_file" for item in data.get("content", []))
        else:
            found = any(call.get("function", {}).get("name") == "read_file" for call in data["choices"][0]["message"].get("tool_calls", []))
    assert found, "fresh read after an edit was not forwarded as a tool call"
    return {"protocol": protocol, "stream": stream, "repeated_read_forwarded": True}


def image_test(args, key):
    from PIL import Image
    result = http(args.base_url + "/images/generations", {"model": "auto", "prompt": "Generate a simple illustration of one red apple on a plain white background.", "n": 1, "size": "1024x1024", "response_format": "b64_json"}, key=key, timeout=240)
    raw = base64.b64decode(result["data"][0]["b64_json"], validate=True)
    with Image.open(io.BytesIO(raw)) as image:
        image.load()
        assert min(image.size) >= 256, "generated image is too small"
        image.convert("RGB").save(args.work_dir / "image.png")
        return {"image_decoded": True, "width": image.width, "height": image.height, "bytes": len(raw), "sha256": hashlib.sha256(raw).hexdigest()}


def image_inspection_test(args, key):
    if args.image is None:
        raise ValueError("inspection mode requires --image pointing to the generated apple fixture")
    encoded = base64.b64encode(args.image.read_bytes()).decode()
    result = http(args.base_url + "/responses", {
        "model": args.model, "stream": False,
        "input": [
            {"role": "user", "content": "Describe the main object and its color in the image returned by the tool. Use the image, not the filename."},
            {"type": "function_call", "call_id": "inspect-image", "name": "view_image", "arguments": "{}"},
            {"type": "function_call_output", "call_id": "inspect-image", "output": [
                {"type": "input_text", "text": "Image for visual inspection."},
                {"type": "input_image", "image_url": "data:image/png;base64," + encoded},
            ]},
        ],
        "tools": [{"type": "function", "name": "view_image", "parameters": {"type": "object", "properties": {}}}],
    }, key=key, timeout=180)
    text = " ".join(block.get("text", "") for item in result.get("output", []) if item.get("type") == "message" for block in item.get("content", []))
    assert "apple" in text.lower() and "red" in text.lower(), "image-bearing tool result was not correctly described"
    return {"tool_image_inspected": True, "object_and_color_recognized": True}


CHECK_SCRIPT = '''import json, time
from pathlib import Path
import hris
root = Path(__file__).parent
first = not (root / "started").exists()
(root / "started").touch()
if first:
    time.sleep(18)
extra = (root / "second-task").exists()
third = (root / "third-task").exists()
fourth = (root / "fourth-task").exists()
errors = []
if not hris.eligible_for_benefits(18) or hris.eligible_for_benefits(17):
    errors.append("benefits: age 18 must qualify; age 17 must not")
if extra and hris.net_pay(1000, 200) != 800:
    errors.append("payroll: deductions must be subtracted")
if third and hris.annual_salary(1000) != 12000:
    errors.append("annual salary: twelve months")
if fourth and hris.overtime_pay(2, 100) != 300:
    errors.append("overtime: one and a half times the hourly rate")
with (root / "check-runs.jsonl").open("a", encoding="utf-8") as f:
    f.write(json.dumps({"passed": not errors, "second_task": extra, "tasks": 1+int(extra)+int(third)+int(fourth)}) + "\\n")
print("PASS" if not errors else "FAIL: " + "; ".join(errors))
raise SystemExit(1 if errors else 0)
'''


def fixture(root):
    root.mkdir(parents=True, exist_ok=False)
    (root / "hris.py").write_text("def eligible_for_benefits(age):\n    return age > 18\n\ndef net_pay(gross, deductions):\n    return gross + deductions\n\ndef annual_salary(monthly):\n    return monthly * 10\n\ndef overtime_pay(hours, rate):\n    return hours * rate\n", encoding="utf-8")
    (root / "check.py").write_text(CHECK_SCRIPT, encoding="utf-8")
    (root / "AGENTS.md").write_text("This is a disposable integration fixture. Work only in this directory. Edit hris.py only; do not edit check.py or the harness marker files. Use the supplied Python executable. Do not delegate or use network/MCP services. Run the existing checks to verify fixes. If optional memory tools are unavailable, continue.\n", encoding="utf-8")


def first_prompt(python):
    return f"Fix eligible_for_benefits in hris.py: age 18 must qualify and age 17 must not. First run the existing checks unchanged using PowerShell: & '{python}' check.py . They intentionally fail. Then fix the function and rerun the checks. Work only in this fixture; do not modify check.py. Use a plan/todo tool to track the fix and verification. Finish with the result, not a promise."


SECOND_PROMPT = "Also fix net_pay in hris.py: deductions must be subtracted, so net_pay(1000, 200) must return 800. Keep the benefits fix in scope. Complete both fixes and run the unchanged check.py before finishing."
FOLLOWUPS = [
    ("second-task", SECOND_PROMPT),
    ("third-task", "Add a third queued fix: annual_salary(monthly) in hris.py must use twelve months, so annual_salary(1000) must be 12000. Keep the earlier fixes in scope."),
    ("fourth-task", "Add a fourth queued fix: overtime_pay(hours, rate) in hris.py must pay 1.5 times the hourly rate, so overtime_pay(2, 100) must be 300. Complete and verify all four queued fixes."),
]
STATUS_PROMPT = "Are you done yet?"


def verify_fixture(root):
    assert (root / "check.py").read_text(encoding="utf-8") == CHECK_SCRIPT, "agent changed the acceptance checks"
    runs = [json.loads(line) for line in (root / "check-runs.jsonl").read_text().splitlines()]
    assert len(runs) >= 2 and not runs[0]["passed"], "missing initial failing check"
    assert runs[-1]["passed"] and runs[-1].get("tasks") == 4, "agent stopped before checking all four fixes"
    verified = subprocess.run([sys.executable, "check.py"], cwd=root, capture_output=True, timeout=30)
    assert verified.returncode == 0, "independent verification failed"
    return {"initial_failure_observed": True, "followups_sent_during_first_check": 3, "status_question_sent": True, "agent_check_runs": len(runs), "queued_fixes_verified": 4, "checks_unchanged": True}


def stop_process(process):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(10)


def safe_diagnostic(text, key):
    text = str(text).replace(key, "[redacted]") if key else str(text)
    text = re.sub(r"eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+", "[redacted JWT]", text)
    text = re.sub(r"(?:sk-[A-Za-z0-9_-]{20,}|gh[pousr]_[A-Za-z0-9]+)", "[redacted key]", text)
    return text[-6000:]


def collect_stderr(process, lines):
    for line in process.stderr:
        lines.append(line.rstrip())
        if len(lines) > 30:
            del lines[0]


def opencode_test(args, key, root):
    fixture(root)
    environment = os.environ.copy()
    environment["M365BRIDGE_API_KEY"] = key
    password = secrets.token_hex(24)
    environment["OPENCODE_SERVER_PASSWORD"] = password
    environment["OPENCODE_SERVER_USERNAME"] = "opencode"
    auth_headers = {"Authorization": "Basic " + base64.b64encode(("opencode:" + password).encode()).decode()}
    # Isolated test configuration; does not modify the installed user's settings.
    for name, directory in (("XDG_CONFIG_HOME", "config"), ("XDG_DATA_HOME", "data"), ("XDG_CACHE_HOME", "cache")):
        environment[name] = str(root.parent / ("opencode-" + directory))
    environment["OPENCODE_CONFIG_CONTENT"] = json.dumps({
        "$schema": "https://opencode.ai/config.json", "model": "m365bridge/" + args.model,
        "provider": {"m365bridge": {"npm": "@ai-sdk/openai-compatible", "name": "M365Bridge", "options": {"baseURL": args.base_url, "apiKey": "{env:M365BRIDGE_API_KEY}"}, "models": {args.model: {"name": args.model, "limit": {"context": 128000, "output": 16000}}}}},
        "permission": {"read": "allow", "edit": "allow", "bash": "allow", "task": "deny", "external_directory": "deny"},
        "mcp": {"engraphis": {"enabled": False}},
    })
    process = subprocess.Popen([args.opencode, "serve", "--pure", "--hostname", "127.0.0.1", "--port", str(args.opencode_port)], cwd=root, env=environment, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True, encoding="utf-8", errors="replace")
    stderr = []
    threading.Thread(target=collect_stderr, args=(process, stderr), daemon=True).start()
    base = f"http://127.0.0.1:{args.opencode_port}"
    query = "?directory=" + urllib.parse.quote(str(root))
    try:
        deadline = time.monotonic() + 60
        while True:
            try:
                http(base + "/global/health", timeout=2, extra_headers=auth_headers)
                break
            except Exception:
                if process.poll() is not None or time.monotonic() > deadline:
                    raise RuntimeError("OpenCode test server did not start: " + safe_diagnostic("\n".join(stderr), key)) from None
                time.sleep(1)
        session = http(base + "/session" + query, {"title": "Bridge continuity integration fixture"}, extra_headers=auth_headers)["id"]
        endpoint = base + "/session/" + session + "/prompt_async" + query
        model = {"providerID": "m365bridge", "modelID": args.model}
        http(endpoint, {"model": model, "parts": [{"type": "text", "text": first_prompt(sys.executable)}]}, extra_headers=auth_headers)
        deadline = time.monotonic() + args.timeout
        sent = False
        while time.monotonic() < deadline:
            if not sent and (root / "started").exists():
                for marker, prompt in FOLLOWUPS:
                    (root / marker).touch()
                    http(endpoint, {"model": model, "parts": [{"type": "text", "text": prompt}]}, extra_headers=auth_headers)
                http(endpoint, {"model": model, "parts": [{"type": "text", "text": STATUS_PROMPT}]}, extra_headers=auth_headers)
                sent = True
            states = http(base + "/session/status" + query, extra_headers=auth_headers)
            if sent and states.get(session, {}).get("type", "idle") == "idle":
                time.sleep(2)
                if http(base + "/session/status" + query, extra_headers=auth_headers).get(session, {}).get("type", "idle") == "idle":
                    return {"client": "opencode", "session": session, **verify_fixture(root)}
            time.sleep(1)
        raise TimeoutError("OpenCode did not finish the multi-task fixture within the test deadline")
    finally:
        stop_process(process)
        (root.parent / "opencode-stderr.txt").write_text(safe_diagnostic("\n".join(stderr), key).replace(password, "[redacted]"), encoding="utf-8")


class RPC:
    def __init__(self, process):
        self.process, self.events, self.next_id = process, queue.Queue(), 0
        def read():
            for line in process.stdout:
                try:
                    self.events.put(json.loads(line))
                except ValueError:
                    pass
        threading.Thread(target=read, daemon=True).start()
        self.notifications = []
        self.observed = []

    def send(self, method, params, notification=False):
        self.next_id += 1
        value = {"method": method, "params": params}
        if not notification:
            value["id"] = self.next_id
        self.process.stdin.write(json.dumps(value) + "\n")
        self.process.stdin.flush()
        return self.next_id

    def call(self, method, params, timeout=60):
        expected = self.send(method, params)
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            event = self.events.get(timeout=max(0.1, deadline - time.monotonic()))
            if event.get("id") == expected and "method" not in event:
                if "error" in event:
                    raise RuntimeError(f"Codex RPC {method} failed: {event['error'].get('code')}")
                return event["result"]
            self.notifications.append(event)
            self.observed.append(event)
        raise TimeoutError("Codex RPC timeout: " + method)


def codex_test(args, key, root):
    fixture(root)
    # Use the installed sandbox helpers: Codex deliberately refuses to install
    # executable helpers in a CODEX_HOME under Windows Temp. All overrides below
    # are process/thread-local; the user's configuration is never rewritten.
    environment = os.environ.copy()
    environment["M365BRIDGE_API_KEY"] = key
    command = [args.codex, "app-server", "--stdio"]
    overrides = {
        "model_provider": "m365bridge", "model": args.model, "model_reasoning_effort": "high",
        "model_catalog_json": str(args.catalog.resolve()),
        "model_providers.m365bridge.name": "M365Bridge",
        "model_providers.m365bridge.base_url": args.base_url,
        "model_providers.m365bridge.env_key": "M365BRIDGE_API_KEY",
        "model_providers.m365bridge.wire_api": "responses",
        "mcp_servers.node_repl.enabled": False, "mcp_servers.engraphis.enabled": False,
        "notify": [],
    }
    for name, value in overrides.items():
        command += ["-c", name + "=" + json.dumps(value)]
    process = subprocess.Popen(command, cwd=root, env=environment, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, encoding="utf-8", errors="replace", bufsize=1)
    stderr = []
    threading.Thread(target=collect_stderr, args=(process, stderr), daemon=True).start()
    rpc = RPC(process)
    try:
        rpc.call("initialize", {"clientInfo": {"name": "bridge-continuity-test", "version": "1"}, "capabilities": {"experimentalApi": True}})
        rpc.send("initialized", {}, notification=True)
        thread = rpc.call("thread/start", {"cwd": str(root), "model": args.model, "modelProvider": "m365bridge", "sandbox": "workspace-write", "approvalPolicy": "never", "approvalsReviewer": "user", "ephemeral": True,
            "config": {"projects": {str(root): {"trust_level": "trusted"}}}})["thread"]["id"]
        turn = rpc.call("turn/start", {"threadId": thread, "input": [{"type": "text", "text": first_prompt(sys.executable)}],
            "sandboxPolicy": {"type": "workspaceWrite", "writableRoots": [str(root)], "networkAccess": False}, "approvalPolicy": "never"})["turn"]["id"]
        deadline, sent = time.monotonic() + args.timeout, False
        while time.monotonic() < deadline:
            if not sent and (root / "started").exists():
                for marker, prompt in FOLLOWUPS:
                    (root / marker).touch()
                    rpc.call("turn/steer", {"threadId": thread, "expectedTurnId": turn, "input": [{"type": "text", "text": prompt}]})
                rpc.call("turn/steer", {"threadId": thread, "expectedTurnId": turn, "input": [{"type": "text", "text": STATUS_PROMPT}]})
                sent = True
            try:
                event = rpc.notifications.pop(0) if rpc.notifications else rpc.events.get(timeout=1)
            except queue.Empty:
                continue
            rpc.observed.append(event)
            if event.get("method") == "turn/completed" and event.get("params", {}).get("turn", {}).get("id") == turn:
                status = event["params"]["turn"].get("status")
                assert status == "completed", "Codex turn status: " + str(status) + " " + safe_diagnostic(event["params"]["turn"].get("error"), key)
                assert sent, "Codex ended before the follow-up could be delivered"
                return {"client": "codex", "thread": thread, **verify_fixture(root)}
        raise TimeoutError("Codex did not finish the multi-task fixture within the test deadline")
    finally:
        stop_process(process)
        records = []
        for event in rpc.observed:
            params = event.get("params", {})
            item = params.get("item", {})
            records.append({"method": event.get("method"), "item_type": item.get("type"), "status": item.get("status"),
                            "text": safe_diagnostic(item.get("text") or item.get("aggregatedOutput") or params.get("error") or "", key),
                            "command": item.get("command")})
        (root.parent / "codex-events.json").write_text(json.dumps(records, indent=2), encoding="utf-8")
        (root.parent / "codex-stderr.txt").write_text(safe_diagnostic("\n".join(stderr), key), encoding="utf-8")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mode", choices=["api", "opencode", "codex", "images", "inspection"], required=True)
    parser.add_argument("--image", type=Path)
    parser.add_argument("--base-url", default="http://127.0.0.1:8002/v1")
    parser.add_argument("--model", default="gpt-5.6-reasoning")
    parser.add_argument("--work-dir", type=Path, required=True)
    parser.add_argument("--bridge-exe", type=Path)
    parser.add_argument("--bridge-home", type=Path, default=Path(r"C:\m365bridge"))
    parser.add_argument("--opencode", default="opencode")
    parser.add_argument("--opencode-port", type=int, default=4098)
    parser.add_argument("--codex", default="codex")
    parser.add_argument("--catalog", type=Path, default=Path.home() / ".codex/bridge-models.json")
    parser.add_argument("--timeout", type=int, default=600)
    args = parser.parse_args()
    args.work_dir.mkdir(parents=True, exist_ok=False)
    key = key_from_environment()
    bridge, report = None, {"mode": args.mode, "model": args.model, "passed": False}
    if args.bridge_exe:
        report["candidate_sha256"] = hashlib.sha256(args.bridge_exe.read_bytes()).hexdigest()
    try:
        if args.bridge_exe:
            environment = os.environ.copy()
            environment["M365_ENABLE_WEB_UI"] = "false"
            if args.mode == "images":
                environment["M365_BROWSER_IMAGE_ROUTING"] = "1"
            else:
                environment.pop("M365_BROWSER_IMAGE_ROUTING", None)
            port = str(urllib.parse.urlsplit(args.base_url).port)
            bridge = subprocess.Popen([str(args.bridge_exe), "serve", "--port", port], cwd=args.bridge_home, env=environment, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            deadline = time.monotonic() + 20
            while True:
                try:
                    http(args.base_url + "/models", key=key, timeout=2)
                    break
                except Exception:
                    if bridge.poll() is not None or time.monotonic() > deadline:
                        raise RuntimeError("Candidate bridge did not become ready") from None
                    time.sleep(0.5)
        if args.mode == "api":
            report["results"] = []
            for protocol in ("chat", "anthropic", "responses"):
                for stream in (False, True):
                    result = api_test(args.base_url, key, args.model, protocol, stream)
                    report["results"].append(result)
                    print(json.dumps(result), flush=True)
        elif args.mode == "images":
            report["result"] = image_test(args, key)
        elif args.mode == "inspection":
            report["result"] = image_inspection_test(args, key)
        elif args.mode == "opencode":
            report["result"] = opencode_test(args, key, args.work_dir / "fixture")
        else:
            report["result"] = codex_test(args, key, args.work_dir / "fixture")
        report["passed"] = True
    except Exception as error:
        report["error"] = str(error).replace(key, "[redacted]") if key else str(error)
        report["error_type"] = type(error).__name__
    finally:
        if bridge:
            stop_process(bridge)
        (args.work_dir / "report.json").write_text(json.dumps(report, indent=2), encoding="utf-8")
    print(json.dumps(report, indent=2))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
