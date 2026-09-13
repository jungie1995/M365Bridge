package toolcalling

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestProgressAllowsLegitimateRepeatedTools(t *testing.T) {
	for _, name := range []string{"read", "grep", "bash", "run_tests"} {
		t.Run(name, func(t *testing.T) {
			var calls []LedgerCall
			var results []LedgerResult
			for i := range 3 {
				id := fmt.Sprint(i)
				calls = append(calls, LedgerCall{ID: id, Name: name, Arguments: `{}`})
				results = append(results, LedgerResult{ID: id, Content: "unchanged"})
			}
			ledger := BuildLedger(calls, results, 3)
			kept, _ := ledger.FilterRepeated([]ToolCall{{Name: name, Arguments: json.RawMessage(`{}`)}})
			if len(kept) != 1 {
				t.Fatal("ordinary repeated inspection was blocked")
			}
		})
	}
}

func TestProgressResetsAfterAnotherOperation(t *testing.T) {
	calls := []LedgerCall{
		{ID: "a", Name: "run_tests", Arguments: `{}`},
		{ID: "b", Name: "run_tests", Arguments: `{}`},
		{ID: "patch", Name: "apply_patch", Arguments: `{"patch":"fix another bug"}`},
	}
	results := []LedgerResult{{ID: "a", Content: "exit code 1"}, {ID: "b", Content: "exit code 1"}, {ID: "patch", Content: "applied"}}
	kept, _ := BuildLedger(calls, results, 3).FilterRepeated([]ToolCall{{Name: "run_tests", Arguments: json.RawMessage(`{}`)}})
	if len(kept) != 1 {
		t.Fatal("a test rerun after an edit was blocked")
	}
}

func TestProgressUsesTheFullResult(t *testing.T) {
	var calls []LedgerCall
	var results []LedgerResult
	for i := range 8 {
		id := fmt.Sprint(i)
		calls = append(calls, LedgerCall{ID: id, Name: "read", Arguments: `{}`})
		// Changes in the compacted-away middle still constitute progress.
		results = append(results, LedgerResult{ID: id, Content: strings.Repeat("x", 5000) + id + strings.Repeat("y", 5000)})
	}
	kept, _ := BuildLedger(calls, results, 8).FilterRepeated([]ToolCall{{Name: "read", Arguments: json.RawMessage(`{}`)}})
	if len(kept) != 1 {
		t.Fatal("changed tool results were mistaken for a stuck loop")
	}
}

func TestProgressDoesNotMistakePollingForAStalledLoop(t *testing.T) {
	for _, name := range []string{"write_stdin", "functions.wait_agent", "wait"} {
		var calls []LedgerCall
		var results []LedgerResult
		for i := range 8 {
			id := fmt.Sprint(i)
			calls = append(calls, LedgerCall{ID: id, Name: name, Arguments: `{}`})
			results = append(results, LedgerResult{ID: id, Content: "still running"})
		}
		kept, _ := BuildLedger(calls, results, 8).FilterRepeated([]ToolCall{{Name: name, Arguments: json.RawMessage(`{}`)}})
		if len(kept) != 1 {
			t.Fatalf("polling %s was blocked", name)
		}
	}
}
