"""Loopback-only, test-owned network faults between a native client and candidate.

Reports contain counts/timings, never request bodies or credentials. Production
bridge transports have no fault-injection header or configuration backdoor.
"""
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import socket
import threading
import time
from urllib.parse import urlsplit


class FaultProxy:
    def __init__(self, upstream, restart):
        address = urlsplit(upstream)
        assert address.hostname in ("127.0.0.1", "localhost"), "candidate must be local"
        self.host, self.port, self.restart = address.hostname, address.port, restart
        self.lock, self.requests, self.events = threading.Lock(), 0, []
        self.interruptions = 0
        owner = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args):
                pass

            def do_GET(self):
                self.forward(b"", None)

            def do_POST(self):
                body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
                with owner.lock:
                    owner.requests += 1
                    number = owner.requests
                fault = {2:"rate_limit", 6:"unavailable", 10:"partial_disconnect", 16:"bridge_restart"}.get(number)
                if fault in ("rate_limit", "unavailable"):
                    status = 429 if fault == "rate_limit" else 503
                    payload = json.dumps({"error":{"type":"rate_limit_error" if status == 429 else "server_error", "code":"fixture_" + fault,"message":"Controlled recovery test: temporary service failure."}}).encode()
                    owner.record(fault, number)
                    self.send_response(status)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(payload)))
                    self.send_header("Retry-After", "1")
                    self.end_headers()
                    self.wfile.write(payload)
                    return
                self.forward(body, (fault, number))

            def forward(self, body, scheduled):
                connection = http.client.HTTPConnection(owner.host, owner.port, timeout=240)
                fault, number = scheduled or (None, 0)
                headers = {k:v for k,v in self.headers.items() if k.lower() not in ("host","connection","content-length","transfer-encoding")}
                self.close_connection = True
                try:
                    connection.request(self.command, self.path, body=body, headers=headers)
                    response = connection.getresponse()
                    self.send_response(response.status)
                    for name,value in response.getheaders():
                        if name.lower() not in ("connection","transfer-encoding","content-length","server","date"):
                            self.send_header(name,value)
                    self.send_header("Connection", "close")
                    self.send_header("Transfer-Encoding", "chunked")
                    self.end_headers()
                    if fault == "bridge_restart":
                        time.sleep(0.25)
                        owner.record(fault, number)
                        owner.restart()
                        self.disconnect()
                        return
                    streaming = "text/event-stream" in response.getheader("Content-Type", "")
                    while True:
                        chunk = response.readline() if streaming else response.read(65536)
                        if not chunk:
                            break
                        self.wfile.write(f"{len(chunk):x}\r\n".encode() + chunk + b"\r\n")
                        self.wfile.flush()
                        if fault == "partial_disconnect" and visible_event(chunk):
                            owner.record(fault, number)
                            self.disconnect()
                            return
                    self.wfile.write(b"0\r\n\r\n")
                    self.wfile.flush()
                except (OSError, http.client.HTTPException):
                    self.disconnect()
                finally:
                    connection.close()

            def disconnect(self):
                self.close_connection = True
                try:
                    self.connection.shutdown(socket.SHUT_RDWR)
                except OSError:
                    pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.url = f"http://127.0.0.1:{self.server.server_port}/v1"

    def record(self, kind, request):
        with self.lock:
            self.events.append({"fault":kind,"request":request,"time":time.time()})
            if kind in ("partial_disconnect", "bridge_restart"):
                self.interruptions += 1

    def report(self):
        with self.lock:
            return {"requests":self.requests,"injected_faults":list(self.events),"interruptions":self.interruptions}

    def verify(self):
        report = self.report()
        expected = {"rate_limit","unavailable","partial_disconnect","bridge_restart"}
        assert {event["fault"] for event in report["injected_faults"]} == expected, "not all planned network faults were exercised"
        assert report["requests"] >= 20, "native recovery run was too short"
        return report

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(5)


def visible_event(line):
    if not line.startswith(b"data: "):
        return False
    try:
        event = json.loads(line[6:])
    except (ValueError, UnicodeDecodeError):
        return False
    if "delta" in event.get("type", ""):
        return True
    return any(choice.get("delta", {}).get("content") or choice.get("delta", {}).get("tool_calls") or choice.get("delta", {}).get("reasoning_content") for choice in event.get("choices", []))
