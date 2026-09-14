package servers

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/client"
	"github.com/KilimcininKorOglu/M365Bridge/pkg/logging"
	"github.com/KilimcininKorOglu/M365Bridge/pkg/payload"
)

const interruptedStreamCode = "upstream_stream_interrupted"

type interruptedStreamError struct{ cause error }

func (e *interruptedStreamError) Error() string { return "upstream stream interrupted after output" }
func (e *interruptedStreamError) Unwrap() error { return e.cause }

type recoveryPolicy struct {
	attempts       int
	baseDelay      time.Duration
	window         time.Duration
	idle           time.Duration
	attemptTimeout time.Duration
	jitter         bool
}

func defaultRecoveryPolicy() recoveryPolicy {
	attempts := 3
	if value, err := strconv.Atoi(os.Getenv("M365_UPSTREAM_MAX_ATTEMPTS")); err == nil {
		attempts = max(1, min(value, 5))
	}
	return recoveryPolicy{attempts: attempts, baseDelay: 500 * time.Millisecond, window: 20 * time.Second, idle: 3 * time.Minute, attemptTimeout: 5 * time.Minute, jitter: true}
}

type recoveryContextKey struct{}
type recoveryBudget struct {
	mu         sync.Mutex
	requestID  string
	remaining  int
	firstRetry time.Time
}

func withRecoveryBudget(ctx context.Context, id string) context.Context {
	if _, ok := ctx.Value(recoveryContextKey{}).(*recoveryBudget); ok {
		return ctx
	}
	return context.WithValue(ctx, recoveryContextKey{}, &recoveryBudget{requestID: id, remaining: 4})
}

func retryableUpstream(err error) bool {
	if _, ok := errors.AsType[*interruptedStreamError](err); ok {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if status, ok := client.UpstreamStatus(err); ok {
		switch status {
		case 0, 408, 429, 500, 502, 503, 504:
			return true
		default:
			return false
		}
	}
	return retryableTransport(err)
}

func retryableTransport(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, client.ErrConnectionClosed) || errors.Is(err, client.ErrHandshakeFailed) || errors.Is(err, client.ErrEmptyTurn) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}
	if failed, ok := client.TurnFailure(err); ok {
		switch strings.ToLower(failed.Value) {
		case "internalerror", "serviceunavailable", "timeout":
			return true
		}
	}
	return false
}

func (p recoveryPolicy) delay(err error, attempt int) time.Duration {
	if delay, ok := client.UpstreamRetryAfter(err); ok {
		return delay
	}
	if status, ok := client.UpstreamStatus(err); ok && status == http.StatusTooManyRequests {
		return rateLimitRetryAfterSeconds * time.Second
	}
	delay := min(p.baseDelay*time.Duration(1<<attempt), 5*time.Second)
	if p.jitter && delay > 0 {
		delay += time.Duration(rand.Int64N(int64(delay/2) + 1))
	}
	return delay
}

func (p recoveryPolicy) wait(ctx context.Context, err error, attempt int) bool {
	if ctx.Err() != nil || attempt+1 >= p.attempts || !retryableUpstream(err) {
		return false
	}
	delay := p.delay(err, attempt)
	budget, _ := ctx.Value(recoveryContextKey{}).(*recoveryBudget)
	if budget == nil || !budget.reserve(delay, p.window) {
		return false
	}
	_, code := classifyUpstreamError(err)
	logging.Infof("upstream retry: request_id=%s attempt=%d code=%s delay_ms=%d", budget.requestID, attempt+2, code, delay.Milliseconds())
	return waitForResponsesEmptyRetry(ctx, delay) == nil
}

func (b *recoveryBudget) reserve(delay, window time.Duration) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if b.firstRetry.IsZero() {
		b.firstRetry = now
	}
	if b.remaining == 0 || now.Add(delay).After(b.firstRetry.Add(window)) {
		return false
	}
	b.remaining--
	return true
}

// Recovery wraps the transport, never the client's tool executor. A retry uses
// a fresh Microsoft conversation and the complete client-supplied context.
type recoveringBackend struct {
	modelBackend
	policy recoveryPolicy
}

func newRecoveringBackend(backend modelBackend, policy recoveryPolicy) *recoveringBackend {
	return &recoveringBackend{modelBackend: backend, policy: policy}
}

func (b *recoveringBackend) ChatConversation(m []payload.Message, tone, override, conversation, user, tenant string, tools bool) (string, string, []client.ToolCall, string, string, error) {
	return b.ChatConversationContext(context.Background(), m, tone, override, conversation, user, tenant, tools)
}

func (b *recoveringBackend) ChatConversationContext(ctx context.Context, m []payload.Message, tone, override, conversation, user, tenant string, tools bool) (string, string, []client.ToolCall, string, string, error) {
	ctx = withRecoveryBudget(ctx, "")
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", "", nil, "", "", err
		}
		callCtx, cancel := context.WithTimeout(ctx, b.policy.attemptTimeout)
		text, thinking, calls, finish, id, err := b.modelBackend.ChatConversationContext(callCtx, m, tone, override, conversation, user, tenant, tools)
		if callCtx.Err() != nil {
			err = callCtx.Err()
		}
		cancel()
		if err == nil || !b.policy.wait(ctx, err, attempt) {
			return text, thinking, calls, finish, id, err
		}
		conversation = ""
	}
}

func (b *recoveringBackend) ChatConversationStreamGenContext(ctx context.Context, m []payload.Message, tone, override, conversation, user, tenant string, tools bool) <-chan client.StreamChunk {
	ctx = withRecoveryBudget(ctx, "")
	output := make(chan client.StreamChunk)
	go func() {
		defer close(output)
		call := func(callCtx context.Context, id string) <-chan client.StreamChunk {
			return b.modelBackend.ChatConversationStreamGenContext(callCtx, m, tone, override, id, user, tenant, tools)
		}
		b.stream(ctx, conversation, call, output)
	}()
	return output
}

func (b *recoveringBackend) stream(ctx context.Context, conversation string, call responsesStreamCall, output chan<- client.StreamChunk) {
	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil {
			return
		}
		callCtx, cancel := context.WithTimeout(ctx, b.policy.attemptTimeout)
		committed, err := forwardRecoverableAttempt(callCtx, call(callCtx, conversation), output, b.policy.idle)
		cancel()
		if ctx.Err() != nil || err == nil {
			return
		}
		if committed {
			err = &interruptedStreamError{cause: err}
		}
		if committed || !b.policy.wait(ctx, err, attempt) {
			emitRecoveryChunk(ctx, output, client.StreamChunk{Error: err})
			return
		}
		conversation = ""
	}
}

func emitRecoveryChunk(ctx context.Context, output chan<- client.StreamChunk, chunk client.StreamChunk) bool {
	select {
	case output <- chunk:
		return true
	case <-ctx.Done():
		return false
	}
}

func nextRecoveryChunk(ctx context.Context, source <-chan client.StreamChunk, idle <-chan time.Time) (client.StreamChunk, error) {
	select {
	case <-ctx.Done():
		return client.StreamChunk{}, ctx.Err()
	case <-idle:
		return client.StreamChunk{}, context.DeadlineExceeded
	case chunk, ok := <-source:
		if !ok {
			return client.StreamChunk{}, client.ErrConnectionClosed
		}
		return chunk, chunk.Error
	}
}

func forwardRecoverableAttempt(ctx context.Context, source <-chan client.StreamChunk, output chan<- client.StreamChunk, idle time.Duration) (bool, error) {
	timer := time.NewTimer(idle)
	defer timer.Stop()
	committed := false
	for {
		chunk, err := nextRecoveryChunk(ctx, source, timer.C)
		if err != nil {
			return committed, err
		}
		timer.Reset(idle)
		committed = committed || visibleChunk(chunk) || len(chunk.ToolCalls) > 0
		if !emitRecoveryChunk(ctx, output, chunk) {
			return committed, ctx.Err()
		}
		if chunk.IsFinal {
			return committed, nil
		}
	}
}
