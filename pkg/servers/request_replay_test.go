package servers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func addContractTools(protocol string, body map[string]any) {
	definition := map[string]any{"name": "read_board", "parameters": map[string]any{"type": "object", "properties": map[string]any{"board": map[string]any{"type": "string"}}, "required": []string{"board"}}}
	switch protocol {
	case "chat":
		body["tools"] = []any{map[string]any{"type": "function", "function": definition}}
	case "anthropic":
		body["tools"] = []any{map[string]any{"name": "read_board", "input_schema": definition["parameters"]}}
	default:
		definition["type"] = "function"
		body["tools"] = []any{definition}
	}
}

func TestReplayKeepsExactResponseAndToolIdentitiesAcrossRestart(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", protocol, stream), func(t *testing.T) {
				root := t.TempDir()
				api, server := newContractServerAt(t, root)
				backend := &faultBackend{}
				api.m365Client = newRecoveringBackend(backend, fastRecoveryPolicy())
				path, body := protocolRequest(protocol, "SDK_TOOL", stream)
				addContractTools(protocol, body)
				status, headers, first := postContract(t, server.URL, path, body, "logical-turn", "sdk-fixture-key")
				if status != 200 || !bytes.Contains(first, []byte("read_board")) {
					t.Fatalf("first turn failed: %s", first)
				}
				server.Close()
				restarted, next := newContractServerAt(t, root)
				restarted.m365Client = newRecoveringBackend(backend, fastRecoveryPolicy())
				status, replayHeaders, replayed := postContract(t, next.URL, path, body, "logical-turn", "sdk-fixture-key")
				if status != 200 || !bytes.Equal(first, replayed) || headers.Get("X-Request-ID") != replayHeaders.Get("X-Request-ID") {
					t.Fatal("replay changed wire body or identity")
				}
				if backend.calls.Load() != 1 || replayHeaders.Get("Idempotency-Replayed") != "true" {
					t.Fatal("replay executed the model again")
				}
			})
		}
	}
}

func TestConcurrentDuplicateRequestsExecuteOnceAcrossServers(t *testing.T) {
	root := t.TempDir()
	api, first := newContractServerAt(t, root)
	other, second := newContractServerAt(t, root)
	gate := make(chan struct{})
	backend := &faultBackend{gate: gate}
	api.m365Client = newRecoveringBackend(backend, fastRecoveryPolicy())
	other.m365Client = api.m365Client
	path, body := protocolRequest("responses", "SDK_TEXT", false)
	results := make(chan []byte, 8)
	var workers sync.WaitGroup
	for index := range 8 {
		workers.Go(func() {
			url := []string{first.URL, second.URL}[index%2]
			status, _, data := postContract(t, url, path, body, "same-key", "sdk-fixture-key")
			if status != 200 {
				t.Errorf("duplicate request returned %d: %s", status, data)
			}
			results <- data
		})
	}
	awaitBackendCalls(t, backend, 1)
	close(gate)
	workers.Wait()
	close(results)
	var expected []byte
	for data := range results {
		if expected == nil {
			expected = data
		}
		if !bytes.Equal(expected, data) {
			t.Fatal("concurrent duplicates got different results")
		}
	}
	if backend.calls.Load() != 1 {
		t.Fatalf("duplicate execution count=%d", backend.calls.Load())
	}
}

func awaitBackendCalls(t *testing.T, backend *faultBackend, count int32) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for backend.calls.Load() < count && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if backend.calls.Load() < count {
		t.Fatal("backend request did not start")
	}
}

func TestReplayRejectsBodyAndSessionChangesAndSeparatesCredentials(t *testing.T) {
	api, server := newContractServer(t)
	backend := &faultBackend{}
	api.m365Client = newRecoveringBackend(backend, fastRecoveryPolicy())
	path, body := protocolRequest("responses", "SDK_TEXT", false)
	_, _, first := postContract(t, server.URL, path, body, "key", "sdk-fixture-key")
	body["session_id"] = "another-session"
	status, _, data := postContract(t, server.URL, path, body, "key", "sdk-fixture-key")
	if status != 409 || !bytes.Contains(data, []byte("idempotency_conflict")) {
		t.Fatal("session change accepted")
	}
	body["session_id"] = "recovery-responses"
	body["input"] = "changed input"
	status, _, _ = postContract(t, server.URL, path, body, "key", "sdk-fixture-key")
	if status != 409 {
		t.Fatal("body change accepted")
	}
	body["input"] = "SDK_TEXT"
	status, _, other := postContract(t, server.URL, path, body, "key", "sdk-other-key")
	if status != 200 || bytes.Equal(first, other) || backend.calls.Load() != 2 {
		t.Fatal("credential isolation failed")
	}
}

func TestReplayHonorsStorageOptOutBeforeExecution(t *testing.T) {
	for _, globallyDisabled := range []bool{false, true} {
		api, server := newContractServer(t)
		backend := &faultBackend{}
		api.m365Client = backend
		api.continuity.retain = !globallyDisabled
		path, body := protocolRequest("responses", "SDK_TEXT", false)
		body["store"] = globallyDisabled
		status, _, _ := postContract(t, server.URL, path, body, "key", "sdk-fixture-key")
		if status != 400 || backend.calls.Load() != 0 {
			t.Fatal("storage opt-out was ignored")
		}
		files, _ := filepath.Glob(filepath.Join(api.continuity.dir, "*.bin"))
		if len(files) != 0 {
			t.Fatal("opted-out request reached disk")
		}
	}
}

func TestReplayCapacityFailsBeforeCompletionAndKeepsTombstone(t *testing.T) {
	api, _ := newContractServer(t)
	handler := api.withInference(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"sequence_number\":0}\n\n"))
		_, _ = w.Write(bytes.Repeat([]byte("x"), replayBodyMax+1))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\"}\n\n"))
	})
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":"test"}`))
	r.Header.Set("Idempotency-Key", "oversized")
	rec := httptest.NewRecorder()
	handler(rec, r)
	if !strings.Contains(rec.Body.String(), "idempotency_capacity") || strings.Contains(rec.Body.String(), "response.completed") {
		t.Fatal("capacity overflow claimed success")
	}
	r = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":"test"}`))
	r.Header.Set("Idempotency-Key", "oversized")
	rec = httptest.NewRecorder()
	handler(rec, r)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "idempotency_indeterminate") {
		t.Fatal("oversized operation was allowed to execute twice")
	}
}

func TestReplayCrashChild(t *testing.T) {
	root := os.Getenv("BRIDGE_CRASH_FIXTURE_ROOT")
	if root == "" {
		t.Skip("subprocess fixture")
	}
	api, server := newContractServerAt(t, root)
	api.m365Client = &faultBackend{gate: make(chan struct{})}
	fmt.Fprintln(os.Stdout, server.URL)
	select {}
}

func TestProcessCrashNeverReexecutesAnIndeterminateRequest(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReplayCrashChild$", "-test.timeout=10s")
	command.Env = append(os.Environ(), "BRIDGE_CRASH_FIXTURE_ROOT="+root)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	address, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	api, server := newContractServerAt(t, root)
	backend := &faultBackend{}
	api.m365Client = backend
	path, body := protocolRequest("responses", "SDK_TEXT", false)
	encoded, _ := json.Marshal(body)
	finished := make(chan struct{})
	go sendCrashRequest(ctx, strings.TrimSpace(address)+path, encoded, finished)
	awaitRunningReplay(t, api, path)
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-finished
	status, _, data := postContract(t, server.URL, path, body, "crash-key", "sdk-fixture-key")
	if status != 409 || !bytes.Contains(data, []byte("idempotency_indeterminate")) || backend.calls.Load() != 0 {
		t.Fatalf("crashed request was reexecuted: %d %s", status, data)
	}
}

func sendCrashRequest(ctx context.Context, url string, body []byte, finished chan<- struct{}) {
	defer close(finished)
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer sdk-fixture-key")
	r.Header.Set("Idempotency-Key", "crash-key")
	response, err := http.DefaultClient.Do(r)
	if err == nil {
		_ = response.Body.Close()
	}
}

func awaitRunningReplay(t *testing.T, api *APIServer, path string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, nil)
	r.Header.Set("Authorization", "Bearer sdk-fixture-key")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		record, err := api.continuity.loadReplay(context.Background(), api.callerScope(r), path+"\x00crash-key")
		if err == nil && record.State == "running" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("child never durably reserved the request")
}
