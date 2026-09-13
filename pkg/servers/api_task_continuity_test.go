package servers

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/payload"
	"github.com/KilimcininKorOglu/M365Bridge/pkg/toolcalling"
)

func TestFollowUpPreservesUnfinishedGoal(t *testing.T) {
	for _, followUp := range []string{"fix the payroll bug also", "are you done?", "hi"} {
		input := []any{
			goalUserItem(goalContextMarker + "fix employee validation</codex_internal_context>"),
			updateGoalCall("goal"), updateGoalOutput("goal", "in_progress"),
			goalUserItem(followUp),
		}
		if !responsesGoalContinuationOpen(input) {
			t.Fatalf("follow-up %q silently discarded the unfinished goal", followUp)
		}
	}
}

func pendingPlanMessages(t *testing.T, format string, followUp string) []payload.Message {
	t.Helper()
	plan := `{"plan":[{"step":"fix employee validation","status":"in_progress"},{"step":"verify both fixes","status":"pending"}]}`
	switch format {
	case "responses":
		return responsesInputToMessages([]any{
			goalUserItem("fix employee validation"),
			map[string]any{"type": "function_call", "name": "update_plan", "call_id": "plan", "arguments": plan},
			map[string]any{"type": "function_call_output", "call_id": "plan", "output": "Plan updated"},
			goalUserItem(followUp),
		})
	case "anthropic":
		var args any
		_ = json.Unmarshal([]byte(plan), &args)
		encoded, _ := json.Marshal([]any{
			map[string]any{"role": "user", "content": "fix employee validation"},
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "plan", "name": "update_plan", "input": args}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "plan", "content": "Plan updated"}}},
			map[string]any{"role": "user", "content": followUp},
		})
		return decodeMessages(t, string(encoded))
	default:
		encoded, _ := json.Marshal([]any{
			map[string]any{"role": "user", "content": "fix employee validation"},
			map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "plan", "type": "function", "function": map[string]any{"name": "update_plan", "arguments": plan}}}},
			map[string]any{"role": "tool", "tool_call_id": "plan", "content": "Plan updated"},
			map[string]any{"role": "user", "content": followUp},
		})
		return decodeMessages(t, string(encoded))
	}
}

func TestAllFormatsPreserveTaskAWhenTaskBOrStatusArrives(t *testing.T) {
	for _, format := range []string{"chat", "anthropic", "responses"} {
		for _, followUp := range []string{"also fix payroll", "are you done?", "hi"} {
			t.Run(format+"/"+followUp, func(t *testing.T) {
				messages := pendingPlanMessages(t, format, followUp)
				ledger := buildToolLedger(messages)
				if ledger.Rounds != 0 || len(ledger.Tasks.Pending) != 2 {
					t.Fatal("follow-up must reset the round budget without erasing unfinished work")
				}
				_, err := guardToolProgress(ledger, "auto", toolcalling.SimulatedResult{HasPayload: true, Content: "Hi! What would you like to work on?", FinishReason: "stop"})
				if !errors.Is(err, toolcalling.ErrTaskIncomplete) {
					t.Fatalf("generic greeting silently completed unfinished work: %v", err)
				}
				if _, err := guardToolProgress(ledger, "auto", simWithRunTests()); err != nil {
					t.Fatal("continuation was refused:", err)
				}
			})
		}
	}
}

func TestCancellationSurvivesSimulationWrapping(t *testing.T) {
	for _, format := range []string{"chat", "anthropic", "responses"} {
		messages := pendingPlanMessages(t, format, "cancel the previous task")
		if buildToolLedger(messages).Tasks.Unfinished() {
			t.Fatal("cancellation was ignored")
		}
		switch format {
		case "chat":
			injectSimulatedPrompt(&messages, `{}`, "auto", "")
		case "anthropic":
			injectSimulatedPromptAnthropic(&messages, `{}`, "auto", "")
		case "responses":
			injectSimulatedPromptResponses(&messages, `{}`, "auto", "")
		}
		if buildToolLedger(messages).Tasks.Unfinished() {
			t.Fatal("simulation wrapping undid cancellation")
		}
	}
}

func TestCancelledQueueDoesNotReappearOnTheNextRequest(t *testing.T) {
	for _, format := range []string{"chat", "anthropic", "responses"} {
		messages := pendingPlanMessages(t, format, "cancel all tasks")
		messages = append(messages, payload.Message{Role: "user", Content: "hello"})
		if buildToolLedger(messages).Tasks.Unfinished() {
			t.Fatalf("%s resurrected cancelled tasks on a later message", format)
		}
	}
}

func TestContinuationRetriesAreBoundedAndCanRecover(t *testing.T) {
	policy, _ := newResponsesToolPolicy(responsesTestTools(), "auto")
	policy.ledger = buildToolLedger(pendingPlanMessages(t, "responses", "also fix payroll"))
	for _, recover := range []bool{false, true} {
		attempts := 0
		result, err := parseResponsesSimulationWithRetry("I'll do that next.", policy, func() (string, error) {
			attempts++
			if recover {
				return simulatedToolCallEnvelope("read_nonce"), nil
			}
			return "Hi! What would you like to work on?", nil
		}, nil)
		if recover && (err != nil || len(result.toolCalls) != 1 || attempts != 1) {
			t.Fatalf("continuation failed: %v", err)
		}
		if !recover && (!errors.Is(err, toolcalling.ErrTaskIncomplete) || attempts != 2) {
			t.Fatalf("unbounded or false completion: attempts=%d err=%v", attempts, err)
		}
	}
}

func TestExplicitBlockerAndToolChoiceNoneAreHonored(t *testing.T) {
	ledger := buildToolLedger(pendingPlanMessages(t, "chat", "also fix payroll"))
	for _, test := range []struct{ choice, text string }{{"auto", "Blocked: the required dependency is unavailable."}, {"none", "Here is the status you requested."}} {
		_, err := guardToolProgress(ledger, test.choice, toolcalling.SimulatedResult{Content: test.text, HasPayload: true})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestExplicitBlockerEndsAnOpenGoalWithoutPretendingSuccess(t *testing.T) {
	if phase := responsesMessagePhase(true, nil, "Blocked: the required input is missing."); phase != "final_answer" {
		t.Fatalf("blocker would trigger endless goal continuation: %s", phase)
	}
}

func TestDelegatedResultDoesNotCompleteTheParentPlan(t *testing.T) {
	messages := pendingPlanMessages(t, "chat", "are you done?")
	messages = append(messages,
		payload.Message{Role: "assistant", ToolCalls: []payload.ToolCallRecord{{ID: "child", Name: "task", Arguments: `{"description":"inspect payroll"}`}}},
		payload.Message{Role: "tool", ToolCallID: "child", ToolResults: []payload.ToolResultRecord{{ID: "child", Content: `{"completed":true,"result":"Found the bug; parent must apply and verify the fix."}`}}},
	)
	_, err := guardToolProgress(buildToolLedger(messages), "auto", toolcalling.SimulatedResult{Content: "The worker finished.", HasPayload: true})
	if !errors.Is(err, toolcalling.ErrTaskIncomplete) {
		t.Fatal("a child result silently completed the parent's unfinished fix and verification")
	}
	messages = append(messages,
		payload.Message{Role: "assistant", ToolCalls: []payload.ToolCallRecord{{ID: "verified", Name: "update_plan", Arguments: `{"plan":[{"step":"fix employee validation","status":"completed"},{"step":"verify both fixes","status":"completed"}]}`}}},
		payload.Message{Role: "tool", ToolCallID: "verified", ToolResults: []payload.ToolResultRecord{{ID: "verified", Content: "Plan updated"}}},
	)
	if _, err := guardToolProgress(buildToolLedger(messages), "auto", toolcalling.SimulatedResult{Content: "Both fixes were verified.", HasPayload: true}); err != nil {
		t.Fatal("the parent could not finish its acknowledged completed plan:", err)
	}
}

func TestProgressFailuresAreErrorsOnEveryWireFormat(t *testing.T) {
	api := &APIServer{}
	for _, failure := range []error{toolcalling.ErrTaskIncomplete, toolcalling.ErrToolLoopStalled} {
		for _, format := range []string{"json", "chat-sse", "completions-sse", "anthropic-sse", "responses-sse"} {
			rec := httptest.NewRecorder()
			writeProgressTestError(api, rec, format, failure)
			body := rec.Body.String()
			if !strings.Contains(body, failure.Error()) || strings.Contains(body, `"finish_reason":"stop"`) || strings.Contains(body, `"type":"response.completed"`) {
				t.Fatalf("%s concealed a failure as completion: %s", format, body)
			}
			if format == "json" && rec.Code != 409 {
				t.Fatalf("status=%d, want actionable conflict", rec.Code)
			}
		}
	}
}

func writeProgressTestError(api *APIServer, rec *httptest.ResponseRecorder, format string, failure error) {
	switch format {
	case "json":
		api.sendUpstreamError(rec, "tool execution", failure)
	case "chat-sse":
		api.sendSSEError(rec, "tool execution", "turn", "model", failure)
	case "completions-sse":
		api.sendSSEError(rec, "tool execution", "turn", "model", failure, "text_completion")
	case "anthropic-sse":
		api.sendAnthropicProgressError(rec, failure)
	case "responses-sse":
		writeResponsesServerError(rec, true, "turn", 1, "model", failure.Error(), upstreamErrorMessage("tool execution", failure.Error()))
	}
}

func TestFollowUpDoesNotReopenCompletedGoal(t *testing.T) {
	input := []any{
		goalUserItem(goalContextMarker + "fix employee validation</codex_internal_context>"),
		updateGoalCall("goal"), updateGoalOutput("goal", "complete"),
		goalUserItem("thank you"),
	}
	if responsesGoalContinuationOpen(input) {
		t.Fatal("completed work was restarted")
	}
}
