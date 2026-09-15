package servers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/client"
	"github.com/KilimcininKorOglu/M365Bridge/pkg/payload"
)

type faultBackend struct {
	contractBackend
	calls         atomic.Int32
	failures      int32
	failEvery     int32
	failure       error
	partial       bool
	idle          bool
	gate          <-chan struct{}
	active        atomic.Int32
	mu            sync.Mutex
	conversations []string
}

func (b *faultBackend) begin(conversation string) int32 {
	b.mu.Lock()
	b.conversations = append(b.conversations, conversation)
	b.mu.Unlock()
	return b.calls.Add(1)
}

func (b *faultBackend) wait(ctx context.Context) error {
	if b.gate == nil {
		return nil
	}
	select {
	case <-b.gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *faultBackend) ChatConversationContext(ctx context.Context, messages []payload.Message, a, c, conversation, e, f string, tools bool) (string, string, []client.ToolCall, string, string, error) {
	call := b.begin(conversation)
	b.active.Add(1)
	defer b.active.Add(-1)
	if err := b.wait(ctx); err != nil {
		return "", "", nil, "", "", err
	}
	if b.shouldFail(call) {
		return "", "", nil, "", "", b.failure
	}
	return b.contractBackend.ChatConversationContext(ctx, messages, a, c, conversation, e, f, tools)
}

func (b *faultBackend) ChatConversationStreamGenContext(ctx context.Context, messages []payload.Message, _, _, conversation, _, _ string, tools bool) <-chan client.StreamChunk {
	call := b.begin(conversation)
	output := make(chan client.StreamChunk)
	b.active.Add(1)
	go func() {
		defer close(output)
		defer b.active.Add(-1)
		if b.wait(ctx) != nil {
			return
		}
		if b.shouldFail(call) {
			b.emitFailure(ctx, output)
			return
		}
		for _, chunk := range contractChunks(messages, tools) {
			if !emitRecoveryChunk(ctx, output, chunk) {
				return
			}
		}
	}()
	return output
}

func (b *faultBackend) shouldFail(call int32) bool {
	return call <= b.failures || (b.failEvery > 0 && call%b.failEvery == 0)
}

func (b *faultBackend) emitFailure(ctx context.Context, output chan<- client.StreamChunk) {
	if b.partial && !emitRecoveryChunk(ctx, output, client.StreamChunk{Text: "Partial board answer"}) {
		return
	}
	if b.idle {
		<-ctx.Done()
		return
	}
	if b.failure != nil {
		emitRecoveryChunk(ctx, output, client.StreamChunk{Error: b.failure})
	}
}

func fastRecoveryPolicy() recoveryPolicy {
	return recoveryPolicy{attempts: 3, baseDelay: time.Millisecond, window: time.Second, idle: 30 * time.Millisecond, attemptTimeout: time.Second}
}

func protocolRequest(protocol, prompt string, stream bool) (string, map[string]any) {
	body := map[string]any{"model": "gpt-5.6-reasoning", "stream": stream, "session_id": "recovery-" + protocol}
	switch protocol {
	case "responses":
		body["input"] = prompt
		return "/v1/responses", body
	case "anthropic":
		body["max_tokens"] = 1024
		body["messages"] = []any{map[string]any{"role": "user", "content": prompt}}
		return "/v1/messages", body
	default:
		body["messages"] = []any{map[string]any{"role": "user", "content": prompt}}
		return "/v1/chat/completions", body
	}
}

func postContract(t *testing.T, base, path string, body any, key, credential string) (int, http.Header, []byte) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r, err := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+credential)
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, response.Header, data
}

func assertProtocolSuccess(t *testing.T, protocol string, stream bool, status int, body []byte) {
	t.Helper()
	if status != 200 || !bytes.Contains(body, []byte("Board ready")) {
		t.Fatalf("request failed: %d %s", status, body)
	}
	if !stream {
		return
	}
	terminal := map[string]string{"chat": `"finish_reason":"stop"`, "responses": `"type":"response.completed"`, "anthropic": `"type":"message_stop"`}[protocol]
	if !bytes.Contains(body, []byte(terminal)) {
		t.Fatalf("missing successful terminal event: %s", body)
	}
}

func TestTransientRecoveryAcrossProtocols(t *testing.T) {
	faults := map[string]error{
		"empty-turn": client.ErrEmptyTurn,
		"timeout":    context.DeadlineExceeded, "closed": client.ErrConnectionClosed,
		"500": &client.UpstreamError{Status: 500}, "503": &client.UpstreamError{Status: 503},
		"rate-limit": &client.UpstreamError{Status: 429, RetryAfter: new(time.Duration(0))},
	}
	for protocol := range map[string]bool{"chat": true, "responses": true, "anthropic": true} {
		for name, failure := range faults {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", protocol, name, stream), func(t *testing.T) {
					api, server := newContractServer(t)
					backend := &faultBackend{failures: 2, failure: failure}
					api.m365Client = newRecoveringBackend(backend, fastRecoveryPolicy())
					path, body := protocolRequest(protocol, "SDK_TEXT", stream)
					status, _, data := postContract(t, server.URL, path, body, "", "sdk-fixture-key")
					assertProtocolSuccess(t, protocol, stream, status, data)
					if backend.calls.Load() != 3 {
						t.Fatalf("attempts=%d, want3", backend.calls.Load())
					}
				})
			}
		}
	}
}

func TestPermanentFailureAndRetryAfterAreNotHammered(t *testing.T) {
	for _, code := range []int{400, 401, 403, 429} {
		api, server := newContractServer(t)
		backend := &faultBackend{failures: 10, failure: &client.UpstreamError{Status: code, RetryAfter: new(60 * time.Second)}}
		api.m365Client = newRecoveringBackend(backend, fastRecoveryPolicy())
		path, body := protocolRequest("chat", "SDK_TEXT", false)
		status, headers, data := postContract(t, server.URL, path, body, "", "sdk-fixture-key")
		if status < 400 || backend.calls.Load() != 1 {
			t.Fatalf("permanent/delayed failure retried: %d %s", status, data)
		}
		if code == 429 && headers.Get("Retry-After") != "60" {
			t.Fatal("Retry-After was lost")
		}
	}
}

func TestPartialAndMissingTerminalStreamsNeverReplayOrComplete(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		for _, failure := range []error{client.ErrConnectionClosed, client.ErrEmptyTurn, nil} {
			api, server := newContractServer(t)
			backend := &faultBackend{failures: 1, failure: failure, partial: true}
			api.m365Client = newRecoveringBackend(backend, fastRecoveryPolicy())
			path, body := protocolRequest(protocol, "SDK_TEXT", true)
			_, _, data := postContract(t, server.URL, path, body, "", "sdk-fixture-key")
			if !bytes.Contains(data, []byte(interruptedStreamCode)) || backend.calls.Load() != 1 {
				t.Fatalf("partial reply wasn't terminal failure: %s", data)
			}
			for _, marker := range []string{`"finish_reason":"stop"`, `"type":"response.completed"`, `"type":"message_stop"`} {
				if bytes.Contains(data, []byte(marker)) {
					t.Fatalf("false completion after interruption: %s", data)
				}
			}
		}
	}
}

func TestIdleUpstreamRecoveryReleasesTheAbandonedAttempt(t *testing.T) {
	api, server := newContractServer(t)
	backend := &faultBackend{failures: 1, idle: true}
	api.m365Client = newRecoveringBackend(backend, fastRecoveryPolicy())
	path, body := protocolRequest("responses", "SDK_TEXT", true)
	status, _, data := postContract(t, server.URL, path, body, "", "sdk-fixture-key")
	assertProtocolSuccess(t, "responses", true, status, data)
	awaitBackendIdle(t, backend)
	if backend.calls.Load() != 2 {
		t.Fatal("idle timeout was not recovered")
	}
}

func awaitBackendIdle(t *testing.T, backend *faultBackend) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for backend.active.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if backend.active.Load() != 0 {
		t.Fatal("abandoned upstream attempt still active")
	}
}

func TestRetryBudgetIsSharedAcrossARequest(t *testing.T) {
	ctx := withRecoveryBudget(context.Background(), "test")
	backend := &faultBackend{failures: 100, failure: client.ErrConnectionClosed}
	recovery := newRecoveringBackend(backend, fastRecoveryPolicy())
	for range 4 {
		_, _, _, _, _, _ = recovery.ChatConversationContext(ctx, nil, "", "", "old-conversation", "", "", false)
	}
	if backend.calls.Load() != 8 {
		t.Fatalf("nested calls exceeded shared extra-attempt budget: %d", backend.calls.Load())
	}
	for _, id := range backend.conversations {
		if id != "" && id != "old-conversation" {
			t.Fatal("unexpected conversation id")
		}
	}
}

func TestRecoveryPolicyRespectsCancelledBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(withRecoveryBudget(context.Background(), "test"))
	cancel()
	if fastRecoveryPolicy().wait(ctx, client.ErrConnectionClosed, 0) {
		t.Fatal("cancelled request retried")
	}
	if retryableUpstream(errors.New("invalid tool schema")) || retryableUpstream(&client.TurnFailedError{Value: "Forbidden"}) {
		t.Fatal("unclassified failure retried")
	}
	if strings.Contains(upstreamErrorMessage("request", interruptedStreamCode), "access_token") {
		t.Fatal("unsafe error text")
	}
}

func TestExecutionOnlyReplyCannotCompleteAToolTurn(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		for _, stream := range []bool{false, true} {
			_, server := newContractServer(t)
			path, body := protocolRequest(protocol, "SDK_ANNOUNCE", stream)
			addContractTools(protocol, body)
			status, _, data := postContract(t, server.URL, path, body, "", "sdk-fixture-key")
			if !bytes.Contains(data, []byte("task_incomplete")) {
				t.Fatalf("%s announced work as completion: %d %s", protocol, status, data)
			}
		}
	}
}
