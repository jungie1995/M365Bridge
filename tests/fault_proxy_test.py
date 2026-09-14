"""Verify fault injection really truncates HTTP, rather than a clean SSE EOF."""
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
from pathlib import Path
import threading
import unittest
from urllib.parse import urlsplit

spec = importlib.util.spec_from_file_location("fault_proxy", Path(__file__).resolve().parents[1] / "scripts/continuity_fault_proxy.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ProxyTests(unittest.TestCase):
    def test_clean_stream_and_dropped_stream_have_different_http_outcomes(self):
        class Origin(BaseHTTPRequestHandler):
            def log_message(self, *args): pass
            def do_POST(self):
                self.rfile.read(int(self.headers.get("Content-Length", "0")))
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                self.wfile.write(b'data: {"choices":[{"delta":{"content":"hello"}}]}\n\ndata: [DONE]\n\n')
        origin = ThreadingHTTPServer(("127.0.0.1", 0), Origin)
        thread = threading.Thread(target=origin.serve_forever, daemon=True)
        thread.start()
        proxy = module.FaultProxy(f"http://127.0.0.1:{origin.server_port}/v1", lambda: None)
        try:
            address = urlsplit(proxy.url)
            connection = http.client.HTTPConnection(address.hostname, address.port, timeout=5)
            connection.request("POST", "/v1/responses", body=b"{}")
            self.assertIn(b"[DONE]", connection.getresponse().read())
            connection.close()
            proxy.requests = 9
            connection = http.client.HTTPConnection(address.hostname, address.port, timeout=5)
            connection.request("POST", "/v1/responses", body=b"{}")
            with self.assertRaises(http.client.IncompleteRead):
                connection.getresponse().read()
            connection.close()
            self.assertEqual(proxy.interruptions, 1)
        finally:
            proxy.close()
            origin.shutdown()
            origin.server_close()
            thread.join(5)


if __name__ == "__main__":
    unittest.main(verbosity=2)
