"""Disposable, multi-file Kanban acceptance fixture for native coding clients."""
import hashlib
import json
from pathlib import Path
import subprocess
import sys

SERVICE_STUB = '''class Kanban:
    def __init__(self, path):
        self.path = str(path)

    def close(self):
        pass

    def create_board(self, name):
        raise NotImplementedError("implement persistent boards")

    def create_card(self, board_id, title):
        raise NotImplementedError("implement persistent cards")

    def get_card(self, board_id, card_id):
        raise NotImplementedError

    def cards(self, board_id, column=None):
        raise NotImplementedError

    def set_wip(self, board_id, limit):
        raise NotImplementedError

    def move_card(self, board_id, card_id, column, index=0, expected_version=None):
        raise NotImplementedError

    def rename_card(self, board_id, card_id, title, expected_version=None):
        raise NotImplementedError

    def audit(self, board_id):
        raise NotImplementedError
'''

CHECKS = r'''import io
import json
from pathlib import Path
import tempfile
import unittest

from kanban.service import Kanban
from kanban.web import create_app

class ServiceTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.path = Path(self.directory.name) / "boards.sqlite"
        self.service = Kanban(self.path)
        self.board = self.service.create_board("HR Operations")

    def tearDown(self):
        self.service.close()
        self.directory.cleanup()

    def test_create_and_list(self):
        card = self.service.create_card(self.board["id"], "Employee validation")
        self.assertEqual(card["column"], "todo")
        self.assertEqual(card["version"], 1)
        self.assertEqual(self.service.cards(self.board["id"])[0]["id"], card["id"])

    def test_blank_board_name(self):
        with self.assertRaises(ValueError): self.service.create_board("  ")

    def test_blank_card_title(self):
        with self.assertRaises(ValueError): self.service.create_card(self.board["id"], " ")

    def test_unknown_board(self):
        with self.assertRaises(LookupError): self.service.create_card(99999, "Payroll")

    def test_board_isolation(self):
        other = self.service.create_board("Another project")
        card = self.service.create_card(self.board["id"], "Private board card")
        self.assertEqual(self.service.cards(other["id"]), [])
        with self.assertRaises(LookupError): self.service.get_card(other["id"], card["id"])
        with self.assertRaises(LookupError): self.service.move_card(other["id"], card["id"], "doing")

    def test_persistence_after_reopen(self):
        card = self.service.create_card(self.board["id"], "Persisted")
        self.service.close()
        self.service = Kanban(self.path)
        self.assertEqual(self.service.get_card(self.board["id"], card["id"])["title"], "Persisted")

    def test_move_and_version(self):
        card = self.service.create_card(self.board["id"], "Attendance")
        moved = self.service.move_card(self.board["id"], card["id"], "doing", expected_version=1)
        self.assertEqual((moved["column"], moved["version"]), ("doing", 2))

    def test_invalid_column_does_not_mutate(self):
        card = self.service.create_card(self.board["id"], "Payroll")
        with self.assertRaises(ValueError): self.service.move_card(self.board["id"], card["id"], "invalid")
        self.assertEqual(self.service.get_card(self.board["id"], card["id"]), card)

    def test_wip_limit_is_atomic(self):
        self.service.set_wip(self.board["id"], 1)
        a = self.service.create_card(self.board["id"], "First")
        b = self.service.create_card(self.board["id"], "Second")
        self.service.move_card(self.board["id"], a["id"], "doing")
        audit_before = self.service.audit(self.board["id"])
        with self.assertRaises(RuntimeError): self.service.move_card(self.board["id"], b["id"], "doing")
        self.assertEqual(self.service.get_card(self.board["id"], b["id"]), b)
        self.assertEqual(self.service.audit(self.board["id"]), audit_before)

    def test_wip_released_when_card_leaves(self):
        self.service.set_wip(self.board["id"], 1)
        a = self.service.create_card(self.board["id"], "First")
        b = self.service.create_card(self.board["id"], "Second")
        self.service.move_card(self.board["id"], a["id"], "doing")
        self.service.move_card(self.board["id"], a["id"], "done")
        self.assertEqual(self.service.move_card(self.board["id"], b["id"], "doing")["column"], "doing")

    def test_invalid_wip(self):
        with self.assertRaises(ValueError): self.service.set_wip(self.board["id"], 0)

    def test_reorder_dense_positions(self):
        cards = [self.service.create_card(self.board["id"], title) for title in ["A", "B", "C"]]
        self.service.move_card(self.board["id"], cards[2]["id"], "todo", index=0)
        ordered = self.service.cards(self.board["id"], "todo")
        self.assertEqual([c["title"] for c in ordered], ["C", "A", "B"])
        self.assertEqual([c["position"] for c in ordered], [0, 1, 2])

    def test_invalid_position_does_not_mutate(self):
        card = self.service.create_card(self.board["id"], "A")
        with self.assertRaises(ValueError): self.service.move_card(self.board["id"], card["id"], "todo", index=-1)
        self.assertEqual(self.service.get_card(self.board["id"], card["id"]), card)

    def test_stale_move_does_not_mutate(self):
        card = self.service.create_card(self.board["id"], "A")
        current = self.service.move_card(self.board["id"], card["id"], "doing", expected_version=1)
        with self.assertRaises(RuntimeError): self.service.move_card(self.board["id"], card["id"], "done", expected_version=1)
        self.assertEqual(self.service.get_card(self.board["id"], card["id"]), current)

    def test_two_connections_detect_stale_write(self):
        card = self.service.create_card(self.board["id"], "A")
        other = Kanban(self.path)
        try:
            self.service.rename_card(self.board["id"], card["id"], "Changed", expected_version=1)
            with self.assertRaises(RuntimeError): other.rename_card(self.board["id"], card["id"], "Stale", expected_version=1)
        finally: other.close()

    def test_rename_validates_and_versions(self):
        card = self.service.create_card(self.board["id"], "A")
        renamed = self.service.rename_card(self.board["id"], card["id"], "Payroll", expected_version=1)
        self.assertEqual((renamed["title"], renamed["version"]), ("Payroll", 2))
        with self.assertRaises(ValueError): self.service.rename_card(self.board["id"], card["id"], " ")

    def test_audit_persists_and_is_isolated(self):
        card = self.service.create_card(self.board["id"], "A")
        self.service.move_card(self.board["id"], card["id"], "doing")
        other = self.service.create_board("Other")
        actions = [e["action"] for e in self.service.audit(self.board["id"]) if e.get("card_id") == card["id"]]
        self.assertEqual(actions, ["created", "moved"])
        self.assertFalse(any(e.get("card_id") == card["id"] for e in self.service.audit(other["id"])))
        self.service.close()
        self.service = Kanban(self.path)
        self.assertEqual([e["action"] for e in self.service.audit(self.board["id"]) if e.get("card_id") == card["id"]], actions)

class WebTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.path = Path(self.directory.name) / "web.sqlite"
        self.app = create_app(self.path)

    def tearDown(self):
        if hasattr(self.app, "close"): self.app.close()
        self.app = None
        import gc
        gc.collect()
        self.directory.cleanup()

    def request(self, method, path, payload=None):
        body = json.dumps(payload).encode() if payload is not None else b""
        captured = []
        env = {"REQUEST_METHOD": method, "PATH_INFO": path, "QUERY_STRING": "", "CONTENT_TYPE": "application/json", "CONTENT_LENGTH": str(len(body)), "wsgi.input": io.BytesIO(body)}
        output = b"".join(self.app(env, lambda status, headers: captured.append((status, headers))))
        return int(captured[0][0].split()[0]), json.loads(output)

    def test_http_create_list_and_move(self):
        status, board = self.request("POST", "/boards", {"name": "HR"})
        self.assertEqual(status, 201)
        status, card = self.request("POST", f"/boards/{board['id']}/cards", {"title": "Payroll"})
        self.assertEqual(status, 201)
        status, moved = self.request("POST", f"/boards/{board['id']}/cards/{card['id']}/move", {"column": "doing", "index": 0, "version": 1})
        self.assertEqual((status, moved["column"]), (200, "doing"))
        status, listing = self.request("GET", f"/boards/{board['id']}/cards")
        self.assertEqual((status, len(listing["cards"])), (200, 1))

    def test_http_validation_and_not_found(self):
        self.assertEqual(self.request("POST", "/boards", {"name": " "})[0], 400)
        self.assertEqual(self.request("GET", "/boards/999999/cards")[0], 404)

    def test_http_stale_update_is_conflict(self):
        _, board = self.request("POST", "/boards", {"name": "HR"})
        _, card = self.request("POST", f"/boards/{board['id']}/cards", {"title": "Payroll"})
        path = f"/boards/{board['id']}/cards/{card['id']}/move"
        self.assertEqual(self.request("POST", path, {"column": "doing", "version": 1})[0], 200)
        self.assertEqual(self.request("POST", path, {"column": "done", "version": 1})[0], 409)
'''

RUNNER = '''import json, pathlib, time, unittest
root = pathlib.Path(__file__).parent
first = not (root / "started").exists()
(root / "started").touch()
if first: time.sleep(20)
suite = unittest.defaultTestLoader.discover(str(root / "checks"))
result = unittest.TextTestRunner(verbosity=2).run(suite)
with (root / "check-runs.jsonl").open("a", encoding="utf-8") as f:
    f.write(json.dumps({"passed": result.wasSuccessful(), "tests": result.testsRun, "failures": len(result.failures), "errors": len(result.errors)}) + "\\n")
raise SystemExit(0 if result.wasSuccessful() else 1)
'''

FOLLOWUPS = [
    ("second-task", "Add WIP-limit enforcement for doing cards. A rejected move must be atomic: no changed positions, version, or audit event. Keep the earlier CRUD work in scope."),
    ("third-task", "Also implement dense card ordering/reordering and strict board isolation. Moving or reading a card through another board must fail. Keep all pending work."),
    ("fourth-task", "Add optimistic version checks, rename validation, persistent audit events, and complete the WSGI API with proper 400/404/409 status handling. Verify the whole unchanged acceptance suite, including database reopen and two-connection tests."),
]
STATUS_PROMPT = "Are you done yet?"


def create(root: Path):
    root.mkdir(parents=True, exist_ok=False)
    (root / "kanban").mkdir()
    (root / "checks").mkdir()
    (root / "kanban/__init__.py").write_text("", encoding="utf-8")
    (root / "kanban/service.py").write_text(SERVICE_STUB, encoding="utf-8")
    (root / "kanban/storage.py").write_text('"""SQLite storage belongs here."""\n', encoding="utf-8")
    (root / "kanban/web.py").write_text('def create_app(path):\n    raise NotImplementedError("implement WSGI JSON API")\n', encoding="utf-8")
    (root / "checks/test_kanban.py").write_text(CHECKS, encoding="utf-8")
    (root / "check.py").write_text(RUNNER, encoding="utf-8")
    (root / "AGENTS.md").write_text("This is an isolated acceptance fixture. Work only in this folder, using Python's standard library. Implement a SQLite Kanban service and WSGI JSON API. Do not modify check.py, checks/, or harness marker files. Keep planning and execution in this client. Do not delegate or use external network/MCP services. Run the full existing acceptance suite; report blockers truthfully.\n", encoding="utf-8")


def first_prompt(python):
    return f"Build the Kanban project from the provided starter modules. Implement persistent SQLite boards and cards plus a WSGI JSON API using the standard library. Start by running the unchanged acceptance checks with PowerShell: & '{python}' check.py . They intentionally fail. Inspect the fixed interfaces and tests; implement the service/storage/web modules, track the work with a plan if available, and verify your changes. Card fields are id, board_id, title, column, position, version. Columns are todo/doing/done. Validation raises ValueError, missing resources LookupError, WIP/stale-version conflicts RuntimeError. Audit actions include created/moved/renamed. Do not edit the acceptance checks."


def verify(root):
    assert (root / "checks/test_kanban.py").read_text(encoding="utf-8") == CHECKS, "agent changed acceptance checks"
    assert (root / "check.py").read_text(encoding="utf-8") == RUNNER, "agent changed verification runner"
    runs = [json.loads(line) for line in (root / "check-runs.jsonl").read_text().splitlines()]
    assert len(runs) >= 2 and not runs[0]["passed"], "initial failing baseline was not recorded"
    assert runs[-1]["passed"] and runs[-1]["tests"] >= 20, "Kanban work stopped before all acceptance checks passed"
    independent = subprocess.run([sys.executable, "check.py"], cwd=root, capture_output=True, timeout=60)
    assert independent.returncode == 0, "independent Kanban acceptance failed"
    hashes = {str(path.relative_to(root)): hashlib.sha256(path.read_bytes()).hexdigest() for path in (root / "kanban").glob("*.py")}
    return {"fixture": "kanban", "acceptance_tests": runs[-1]["tests"], "agent_test_runs": len(runs), "initial_failure_observed": True,
            "all_acceptance_tests_passed": True, "checks_unchanged": True, "independent_verification": True, "source_hashes": hashes}
