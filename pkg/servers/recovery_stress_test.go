package servers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/client"
)

type stressConversation struct {
	protocol string
	history  []any
	previous string
	lastCall string
}

func (s *stressConversation) plan(id, status string) {
	args, _ := json.Marshal(map[string]any{"plan": []any{map[string]string{"step": "implement board", "status": status}, map[string]string{"step": "verify WIP", "status": status}}})
	s.addCall(id, "update_plan", string(args), "Plan updated")
}

func (s *stressConversation) addCall(id, name, args, result string) {
	switch s.protocol {
	case "responses":
		s.history = append(s.history, map[string]any{"type": "function_call", "call_id": id, "name": name, "arguments": args}, map[string]any{"type": "function_call_output", "call_id": id, "output": result})
	case "anthropic":
		var arguments any
		_ = json.Unmarshal([]byte(args), &arguments)
		s.history = append(s.history, map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": arguments}}}, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": result}}})
	default:
		s.history = append(s.history, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": args}}}}, map[string]any{"role": "tool", "tool_call_id": id, "content": result})
	}
}

func (s *stressConversation) request() (string, map[string]any) {
	path, body := protocolRequest(s.protocol, "", false)
	addContractTools(s.protocol, body)
	if s.protocol == "responses" {
		body["input"] = s.history
		if s.previous != "" {
			body["previous_response_id"] = s.previous
		}
	} else {
		body["messages"] = s.history
	}
	return path, body
}

func (s *stressConversation) accept(t *testing.T, data []byte, turn int) {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	result := fmt.Sprintf("Board alpha is now version %d; client verified the change.", turn)
	switch s.protocol {
	case "responses":
		s.acceptResponses(t, response, result)
	case "anthropic":
		s.acceptAnthropic(t, response, result)
	default:
		s.acceptChat(t, response, result)
	}
}

func (s *stressConversation) acceptResponses(t *testing.T, response map[string]any, result string) {
	t.Helper()
	for _, item := range response["output"].([]any) {
		call := item.(map[string]any)
		if call["type"] != "function_call" {
			continue
		}
		s.lastCall = call["call_id"].(string)
		s.previous = response["id"].(string)
		s.history = []any{map[string]any{"type": "function_call_output", "call_id": s.lastCall, "output": result}}
		return
	}
	t.Fatal("long Responses session stopped instead of requesting the next client tool")
}

func (s *stressConversation) acceptAnthropic(t *testing.T, response map[string]any, result string) {
	t.Helper()
	for _, item := range response["content"].([]any) {
		call := item.(map[string]any)
		if call["type"] != "tool_use" {
			continue
		}
		args, _ := json.Marshal(call["input"])
		s.lastCall = call["id"].(string)
		s.addCall(s.lastCall, call["name"].(string), string(args), result)
		return
	}
	t.Fatal("long Anthropic session lost its pending queue")
}

func (s *stressConversation) acceptChat(t *testing.T, response map[string]any, result string) {
	t.Helper()
	message := response["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	calls, _ := message["tool_calls"].([]any)
	if len(calls) == 0 {
		t.Fatal("long Chat session lost its pending queue")
	}
	call := calls[0].(map[string]any)
	function := call["function"].(map[string]any)
	s.lastCall = call["id"].(string)
	s.addCall(s.lastCall, function["name"].(string), function["arguments"].(string), result)
}

func (s *stressConversation) compact(t *testing.T, url string, turn int) {
	t.Helper()
	if s.protocol != "responses" {
		// Simulate a client-supplied summary dropping its planning history. The
		// explicit session checkpoint must supply the exact unfinished labels.
		s.history = []any{map[string]any{"role": "user", "content": "SDK_CHECK_TASKS: client compacted earlier work; continue board alpha."}}
		return
	}
	_, body := s.request()
	delete(body, "tools")
	status, _, data := postContract(t, url, "/v1/responses/compact", body, fmt.Sprintf("compact-%d", turn), "sdk-fixture-key")
	if status != 200 {
		t.Fatalf("compaction at turn%d failed: %s", turn, data)
	}
	var response map[string]any
	_ = json.Unmarshal(data, &response)
	s.previous = ""
	s.history = response["output"].([]any)
}

func TestLongSessionFaultInjection140ToolTurnsPerProtocol(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			root := t.TempDir()
			api, server := newContractServerAt(t, root)
			backend := &faultBackend{failEvery: 17, failure: &client.UpstreamError{Status: 503}}
			api.m365Client = newRecoveringBackend(backend, fastRecoveryPolicy())
			conversation := stressConversation{protocol: protocol, history: []any{map[string]any{"role": "user", "content": "SDK_CHECK_TASKS: continue working on board alpha."}}}
			conversation.plan("initial-plan", "pending")
			seen := map[string]bool{}
			for turn := range 140 {
				path, body := conversation.request()
				status, _, data := postContract(t, server.URL, path, body, fmt.Sprintf("turn-%d", turn), "sdk-fixture-key")
				if status != 200 {
					t.Fatalf("turn%d failed: %d %s", turn, status, data)
				}
				conversation.accept(t, data, turn)
				if seen[conversation.lastCall] {
					t.Fatal("tool call identity reused")
				}
				seen[conversation.lastCall] = true
				if (turn+1)%25 == 0 {
					conversation.compact(t, server.URL, turn)
				}
				if (turn+1)%40 == 0 {
					server.Close()
					api, server = newContractServerAt(t, root)
					api.m365Client = newRecoveringBackend(backend, fastRecoveryPolicy())
				}
			}
			assertStressQueue(t, api, protocol)
			awaitBackendIdle(t, backend)
			t.Logf("verified140unique client tool calls,5compactions,3restarts,%dbackend attempts", backend.calls.Load())
		})
	}
}

func assertStressQueue(t *testing.T, api *APIServer, protocol string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer sdk-fixture-key")
	state, err := api.continuity.checkpoint(context.Background(), api.callerScope(r), "recovery-"+protocol, checkpointUpdate{})
	if err != nil || len(state.Pending) != 2 {
		t.Fatalf("long session lost acknowledged tasks: %+v %v", state, err)
	}
	r.Header.Set("Authorization", "Bearer sdk-other-key")
	other, err := api.continuity.checkpoint(context.Background(), api.callerScope(r), "recovery-"+protocol, checkpointUpdate{})
	if err != nil || other.Unfinished() {
		t.Fatal("long session crossed credential scopes")
	}
}
