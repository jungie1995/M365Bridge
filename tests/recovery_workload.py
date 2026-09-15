"""Four queued, independently verified coding rounds for the native-client soak."""
import json
from pathlib import Path
import subprocess
import sys

SOURCE = '''def column_counts(cards):
    raise NotImplementedError

def wip_remaining(cards, limit):
    raise NotImplementedError

def ordered_titles(cards, column):
    raise NotImplementedError

def stale_ids(cards, expected_versions):
    raise NotImplementedError
'''

CHECKS = '''import copy
from pathlib import Path
import unittest
from kanban import reporting

ROOT = Path(__file__).resolve().parents[1]

@unittest.skipUnless((ROOT / "recovery-round-1").exists(), "round 1 not requested yet")
class Counts(unittest.TestCase):
    def test_empty(self): self.assertEqual(reporting.column_counts([]), {"todo":0,"doing":0,"done":0})
    def test_counts(self): self.assertEqual(reporting.column_counts([{"column":"todo"},{"column":"doing"},{"column":"todo"}]), {"todo":2,"doing":1,"done":0})
    def test_invalid(self):
        with self.assertRaises(ValueError): reporting.column_counts([{"column":"invalid"}])
    def test_no_mutation(self):
        cards=[{"column":"done","title":"A"}]; before=copy.deepcopy(cards)
        reporting.column_counts(cards); self.assertEqual(cards,before)

@unittest.skipUnless((ROOT / "recovery-round-2").exists(), "round 2 not requested yet")
class Capacity(unittest.TestCase):
    def test_empty(self): self.assertEqual(reporting.wip_remaining([],3),3)
    def test_only_doing(self): self.assertEqual(reporting.wip_remaining([{"column":"todo"},{"column":"doing"},{"column":"done"}],3),2)
    def test_zero_floor(self): self.assertEqual(reporting.wip_remaining([{"column":"doing"}]*3,1),0)
    def test_invalid_limit(self):
        for limit in [0,-1,True,1.5]:
            with self.assertRaises(ValueError): reporting.wip_remaining([],limit)

@unittest.skipUnless((ROOT / "recovery-round-3").exists(), "round 3 not requested yet")
class Ordering(unittest.TestCase):
    def test_empty(self): self.assertEqual(reporting.ordered_titles([],"todo"),[])
    def test_filter_and_order(self): self.assertEqual(reporting.ordered_titles([{"title":"B","column":"todo","position":1},{"title":"ignore","column":"done","position":0},{"title":"A","column":"todo","position":0}],"todo"),["A","B"])
    def test_invalid_column(self):
        with self.assertRaises(ValueError): reporting.ordered_titles([],"missing")
    def test_no_mutation(self):
        cards=[{"title":"B","column":"todo","position":1},{"title":"A","column":"todo","position":0}]; before=copy.deepcopy(cards)
        reporting.ordered_titles(cards,"todo"); self.assertEqual(cards,before)

@unittest.skipUnless((ROOT / "recovery-round-4").exists(), "round 4 not requested yet")
class Staleness(unittest.TestCase):
    def test_empty(self): self.assertEqual(reporting.stale_ids([],{}),[])
    def test_versions(self): self.assertEqual(reporting.stale_ids([{"id":2,"version":3},{"id":1,"version":1}],{2:2,1:1}),[2])
    def test_missing_and_sorted(self): self.assertEqual(reporting.stale_ids([{"id":3,"version":2},{"id":1,"version":1},{"id":2,"version":1}],{1:0}),[1,2,3])
    def test_no_mutation(self):
        cards=[{"id":2,"version":3}]; expected={2:1}; before=copy.deepcopy((cards,expected))
        reporting.stale_ids(cards,expected); self.assertEqual((cards,expected),before)
'''

REQUIREMENTS = [
    "Implement kanban/reporting.py column_counts(cards): return counts for all three columns, reject invalid columns with ValueError, and preserve the input.",
    "Implement wip_remaining(cards, limit): count doing cards only, return remaining capacity floored at zero, reject nonpositive/noninteger/bool limits with ValueError.",
    "Implement ordered_titles(cards, column): validate the column, filter its cards, sort by position, return titles without mutating input.",
    "Implement stale_ids(cards, expected_versions): return sorted IDs with missing or mismatched expected versions, preserving both inputs.",
]

class Workload:
    def __init__(self, root, rounds):
        self.root, self.rounds, self.issued = root, rounds, 0
        if rounds:
            (root / "kanban/reporting.py").write_text(SOURCE, encoding="utf-8")
            (root / "checks/test_recovery_rounds.py").write_text(CHECKS, encoding="utf-8")

    def next(self):
        if self.issued >= self.rounds:
            return None
        prompt = REQUIREMENTS[self.issued]
        self.issued += 1
        (self.root / f"recovery-round-{self.issued}").touch()
        return prompt + f" This is queued coding round {self.issued}. Inspect the current code and new active checks, maintain the client plan, complete the change, and run the full unchanged check.py using & '{sys.executable}' check.py before finishing. Preserve the Kanban behavior and earlier rounds."

    def verify(self):
        if not self.rounds:
            return {}
        assert self.issued == self.rounds
        assert (self.root / "checks/test_recovery_rounds.py").read_text(encoding="utf-8") == CHECKS, "recovery acceptance checks were changed"
        result = subprocess.run([sys.executable, "check.py"], cwd=self.root, capture_output=True, timeout=60)
        assert result.returncode == 0, "independent recovery-round verification failed"
        runs = [json.loads(line) for line in (self.root / "check-runs.jsonl").read_text().splitlines()]
        assert runs[-1]["tests"] == 36 and runs[-1]["passed"], "missing full recovery acceptance suite"
        return {"additional_coding_rounds":self.rounds, "recovery_checks_unchanged":True, "total_acceptance_tests":36}
