package toolcalling

import (
	"fmt"
	"strings"
	"testing"
)

func TestTaskQueueAccumulatesFourRequestsAndRetainsOmittedItems(t *testing.T) {
	var calls []LedgerCall
	var results []LedgerResult
	for i := range 4 {
		id := fmt.Sprintf("plan-%d", i)
		calls = append(calls, LedgerCall{ID: id, Name: "update_plan", Arguments: fmt.Sprintf(`{"plan":[{"step":"task-%d","status":"pending"}]}`, i)})
		results = append(results, LedgerResult{ID: id, Content: "Plan updated"})
	}
	state := TasksFromHistory(calls, results)
	if strings.Join(state.Pending, ",") != "task-0,task-1,task-2,task-3" {
		t.Fatalf("additional requests replaced unfinished work: %#v", state.Pending)
	}
	calls = append(calls, LedgerCall{ID: "done-a", Name: "update_plan", Arguments: `{"plan":[{"step":"task-0","status":"completed"}]}`})
	results = append(results, LedgerResult{ID: "done-a", Content: "Plan updated"})
	state = TasksFromHistory(calls, results)
	if strings.Join(state.Pending, ",") != "task-1,task-2,task-3" {
		t.Fatalf("completing one task discarded later queued work: %#v", state.Pending)
	}
}

func TestTaskStateRequiresSuccessfulAcknowledgement(t *testing.T) {
	call := LedgerCall{ID: "plan", Name: "functions.update_plan", Arguments: `{"plan":[{"step":"fix employee validation","status":"in_progress"},{"step":"test payroll","status":"pending"}]}`}
	for name, results := range map[string][]LedgerResult{
		"pending call":       nil,
		"unrelated result":   {{ID: "other", Content: "Plan updated"}},
		"failed call":        {{ID: "plan", Content: "Error: invalid arguments"}},
		"structured failure": {{ID: "plan", Content: `{"is_error":true}`}},
		"Anthropic failure":  {{ID: "plan", Content: "Plan updated", IsError: true}},
	} {
		t.Run(name, func(t *testing.T) {
			if TasksFromHistory([]LedgerCall{call}, results).Unfinished() {
				t.Fatal("an unacknowledged/failed plan became active")
			}
		})
	}
	state := TasksFromHistory([]LedgerCall{call}, []LedgerResult{{ID: "plan", Content: "Plan updated"}})
	if len(state.Pending) != 2 || !strings.Contains(state.Note(), "test payroll") {
		t.Fatalf("acknowledged work was lost: %#v", state)
	}
}

func TestTaskStateKeepsLatestValidPlan(t *testing.T) {
	initial := LedgerCall{ID: "a", Name: "todowrite", Arguments: `{"todos":[{"content":"fix error handling","status":"in_progress","priority":"high"}]}`}
	for name, next := range map[string]string{
		"null": `{"todos":null}`, "missing": `{}`, "malformed": `{"todos":`,
		"unknown status": `{"todos":[{"content":"fix error handling","status":"perhaps"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			calls := []LedgerCall{initial, {ID: "b", Name: "todowrite", Arguments: next}}
			state := TasksFromHistory(calls, []LedgerResult{{ID: "a", Content: "updated"}, {ID: "b", Content: "updated"}})
			if !state.Unfinished() {
				t.Fatal("malformed plan erased unfinished work")
			}
		})
	}
	completed := LedgerCall{ID: "done", Name: "todowrite", Arguments: `{"todos":[{"content":"fix error handling","status":"completed"}]}`}
	state := TasksFromHistory([]LedgerCall{initial, completed}, []LedgerResult{{ID: "a", Content: "updated"}, {ID: "done", Content: "updated"}})
	if state.Unfinished() {
		t.Fatal("completed plan stayed open")
	}
}

func TestTaskEvidenceIsBounded(t *testing.T) {
	state := TaskState{}
	for range 100 {
		state.Pending = append(state.Pending, strings.Repeat("inspect", 1000))
	}
	if len(state.Note()) > 8000 || !strings.Contains(state.Note(), "100 open items") {
		t.Fatal("task evidence is unbounded or loses the full open-task count")
	}
}

func TestExplicitCancellationAndBlocker(t *testing.T) {
	for _, text := range []string{"Stop.", "cancel the previous task", "cancel all tasks"} {
		if !TaskCancellation(text) {
			t.Fatalf("cancellation not recognized: %q", text)
		}
	}
	for _, text := range []string{"fix the cancel button", "also fix payroll", "do not stop", "are you done?"} {
		if TaskCancellation(text) {
			t.Fatalf("ordinary follow-up cancelled work: %q", text)
		}
	}
	if !TaskBlocked("**Blocked:** I need the missing test credentials.") || TaskBlocked("I will test this next.") {
		t.Fatal("blocker handling confused a progress announcement with a blocker")
	}
}
