package servers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/client"
	"github.com/KilimcininKorOglu/M365Bridge/pkg/models"
	"github.com/KilimcininKorOglu/M365Bridge/pkg/payload"
	"github.com/KilimcininKorOglu/M365Bridge/pkg/toolcalling"
)

func acknowledgedPlanMessages(id, label, status string) []payload.Message {
	arguments, _ := json.Marshal(map[string]any{"plan": []any{map[string]string{"step": label, "status": status}}})
	return []payload.Message{
		{Role: "assistant", ToolCalls: []payload.ToolCallRecord{{ID: id, Name: "update_plan", Arguments: string(arguments)}}},
		{Role: "tool", ToolCallID: id, ToolResults: []payload.ToolResultRecord{{ID: id, Content: "Plan updated"}}},
	}
}

func TestCheckpointCancellationRetiresOldPlansButAllowsLaterWork(t *testing.T) {
	store := newContinuityStore(t.TempDir())
	ctx := context.Background()
	old := append([]payload.Message{{Role: "user", Content: "Build the old board"}}, acknowledgedPlanMessages("old", "old work", "pending")...)
	if _, err := store.checkpoint(ctx, "owner", "session", checkpointUpdateFromMessages(old)); err != nil {
		t.Fatal(err)
	}
	history := append(slices.Clone(old), payload.Message{Role: "user", Content: "cancel all tasks"}, payload.Message{Role: "user", Content: "Build a new board"})
	history = append(history, acknowledgedPlanMessages("new", "new work", "pending")...)
	for range 2 {
		state, err := store.checkpoint(ctx, "owner", "session", checkpointUpdateFromMessages(history))
		if err != nil || !slices.Equal(state.Pending, []string{"new work"}) {
			t.Fatalf("cancellation/replay lost new work: %+v %v", state, err)
		}
	}
	state, err := store.checkpoint(ctx, "owner", "session", checkpointUpdateFromMessages(old))
	if err != nil || !slices.Equal(state.Pending, []string{"new work"}) {
		t.Fatalf("old plan was resurrected: %+v %v", state, err)
	}
	state, err = store.checkpoint(ctx, "owner", "session", checkpointUpdateFromMessages(acknowledgedPlanMessages("finished", "new work", "completed")))
	if err != nil || state.Unfinished() {
		t.Fatalf("new plan could not close: %+v %v", state, err)
	}
}

func TestCompactionPreservesOpenAndClosedGoalsAcrossRepeatedCompaction(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "open", true: "closed"}[closed], func(t *testing.T) {
			store := newContinuityStore(t.TempDir())
			input := []any{map[string]any{"role": "user", "content": goalContextMarker + "Build the Kanban board"}}
			if closed {
				input = append(input, map[string]any{"type": "function_call", "name": "update_goal", "call_id": "goal", "arguments": "{}"}, map[string]any{"type": "function_call_output", "call_id": "goal", "output": `{"goal":{"status":"complete"}}`})
			}
			for range 2 {
				snapshot := captureCompactState(input, responsesInputToMessages(input), "session")
				token, err := store.encodeCompaction(context.Background(), "owner", snapshot)
				if err != nil {
					t.Fatal(err)
				}
				s := responseRequestState{ctx: context.Background(), store: store, owner: "owner"}
				raw, _, err := s.restoreCapsule(token)
				if err != nil {
					t.Fatal(err)
				}
				input = append(raw, map[string]any{"role": "user", "content": "Are you done yet?"})
				if responsesGoalContinuationOpen(input) == closed {
					t.Fatal("compaction changed the acknowledged goal status")
				}
			}
		})
	}
}

func TestCompactionWithoutUserMessagesStillRestoresItsCheckpoint(t *testing.T) {
	store := newContinuityStore(t.TempDir())
	token, err := store.encodeCompaction(context.Background(), "owner", compactState{Summary: "work", Tasks: toolcalling.TaskState{Pending: []string{"verify board"}}})
	if err != nil {
		t.Fatal(err)
	}
	s := responseRequestState{ctx: context.Background(), store: store, owner: "owner"}
	_, messages, err := s.restoreCapsule(token)
	if err != nil || !buildToolLedger(messages).Tasks.Unfinished() {
		t.Fatal("lost checkpoint without user messages", err)
	}
}

func TestCredentialScopedSessionLookupAndReset(t *testing.T) {
	api, _ := newContractServer(t)
	request := httptest.NewRequest(http.MethodGet, "/v1/sessions/shared", nil)
	request.Header.Set("Authorization", "Bearer sdk-fixture-key")
	owner := api.callerScope(request)
	_, err := api.continuity.checkpoint(context.Background(), owner, "shared", checkpointUpdateFromMessages(acknowledgedPlanMessages("plan", "private work", "pending")))
	if err != nil {
		t.Fatal(err)
	}
	sid := api.sessionCacheID(request, "shared")
	api.ctxCache.Set(sessionKeyPrefix+sid, "conversation")
	other := request.Clone(context.Background())
	other.Header.Set("Authorization", "Bearer sdk-other-key")
	if api.sessionCacheID(other, "shared") == sid {
		t.Fatal("credentials share a session key")
	}
	rec := httptest.NewRecorder()
	api.handleSession(rec, other)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("other owner read a session: %d", rec.Code)
	}
	request.Method = http.MethodDelete
	request.URL.RawQuery = "local_only=true"
	rec = httptest.NewRecorder()
	api.handleSession(rec, request)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset failed: %s", rec.Body.String())
	}
	state, err := newContinuityStore(api.continuity.dir).checkpoint(context.Background(), owner, "shared", checkpointUpdate{})
	if err != nil || state.Unfinished() {
		t.Fatal("reset queue survived restart", err)
	}
}

func TestLegacySessionsRemainAvailableOnlyToSingleCredentialInstallations(t *testing.T) {
	api, _ := newContractServer(t)
	api.ctxCache.Set(sessionKeyPrefix+"legacy", "conversation")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer sdk-fixture-key")
	if _, visible := api.visibleSessionID(r, "legacy"); visible {
		t.Fatal("unowned history exposed with multiple credentials")
	}
	api.config.APIKeys = []string{"sdk-fixture-key"}
	if api.sessionCacheID(r, "legacy") != "legacy" {
		t.Fatal("single-owner legacy mapping was lost")
	}
	api.config.WebUIPassword = "different-browser-owner"
	if _, visible := api.visibleSessionID(r, "legacy"); visible {
		t.Fatal("browser credential bypassed owner isolation")
	}
}

func TestStoreFalseAndOperatorOptOutDoNotRetainResponseContent(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		api, _ := newContractServer(t)
		api.continuity.retain = !disabled
		body := `{"model":"gpt-5.6-reasoning","input":"SDK_TEXT","store":false,"session_id":"private"}`
		if disabled {
			body = strings.Replace(body, `"store":false`, `"store":true`, 1)
		}
		r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer sdk-fixture-key")
		rec := httptest.NewRecorder()
		api.handleResponses(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("request failed: %s", rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"store":false`) {
			t.Fatal("store opt-out was not reported")
		}
		files, err := filepath.Glob(filepath.Join(api.continuity.dir, "*.bin"))
		if err != nil || len(files) != 0 {
			t.Fatal("opted-out message content reached disk", err)
		}
	}
}

func TestContinuityErrorsDoNotDiscloseLocalPaths(t *testing.T) {
	err := &os.PathError{Op: "open", Path: "/private/credentials/state.key", Err: errors.New("sensitive detail")}
	rec := httptest.NewRecorder()
	api := &APIServer{}
	api.sendContinuityError(rec, err)
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "private") || strings.Contains(rec.Body.String(), "sensitive") {
		t.Fatalf("unsafe storage error: %s", rec.Body.String())
	}
}

type cancelledContractBackend struct {
	modelBackend
	started chan struct{}
	stopped chan struct{}
}

func (b *cancelledContractBackend) ChatConversationContext(ctx context.Context, _ []payload.Message, _, _, _, _, _ string, _ bool) (string, string, []client.ToolCall, string, string, error) {
	close(b.started)
	<-ctx.Done()
	close(b.stopped)
	return "", "", nil, "", "", ctx.Err()
}

func (b *cancelledContractBackend) ChatConversationStreamGenContext(ctx context.Context, _ []payload.Message, _, _, _, _, _ string, _ bool) <-chan client.StreamChunk {
	result := make(chan client.StreamChunk)
	go func() {
		defer close(result)
		close(b.started)
		<-ctx.Done()
		close(b.stopped)
	}()
	return result
}

func TestClientDisconnectCancelsUpstreamBufferedAndStreamingRequests(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		for _, stream := range []bool{false, true} {
			api, server := newContractServer(t)
			backend := &cancelledContractBackend{modelBackend: api.m365Client, started: make(chan struct{}), stopped: make(chan struct{})}
			api.m365Client = newRecoveringBackend(backend, defaultRecoveryPolicy())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			path, request := protocolRequest(protocol, "SDK_WAIT", stream)
			body, _ := json.Marshal(request)
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+path, strings.NewReader(string(body)))
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Authorization", "Bearer sdk-fixture-key")
			r.Header.Set("Idempotency-Key", "cancelled-request")
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				response, err := http.DefaultClient.Do(r)
				if err == nil {
					_, _ = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
				}
			}()
			awaitContractSignal(t, backend.started)
			cancel()
			awaitContractSignal(t, backend.stopped)
			awaitContractSignal(t, finished)
			status, _, replay := postContract(t, server.URL, path, request, "cancelled-request", "sdk-fixture-key")
			if status != 409 || !strings.Contains(string(replay), "idempotency_indeterminate") {
				t.Fatal("cancelled request was replayed as success or reexecuted")
			}
		}
	}
}

func awaitContractSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("request cancellation did not propagate")
	}
}

func TestBufferedResponsesStreamHasMatchingItemLifecycle(t *testing.T) {
	api := &APIServer{}
	rec := httptest.NewRecorder()
	api.respondBufferedResponses(rec, toolLoopResult{text: "Verified", thinking: "Summary", finishReason: "stop"}, nil, models.ModelConfig{OpenAIID: "fixture"}, 0, true, nil, false, nil, "auto")
	for _, event := range []string{"response.output_item.added", "response.output_item.done", "response.output_text.delta", "response.reasoning_summary_text.delta", "response.completed"} {
		if !strings.Contains(rec.Body.String(), "event: "+event+"\n") {
			t.Fatalf("buffered stream lacks %s", event)
		}
	}
}
