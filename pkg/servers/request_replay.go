package servers

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/logging"
	"github.com/gofrs/flock"
	"github.com/google/uuid"
)

const replayBodyMax = 2 << 20

var errReplayBusy = errors.New("idempotency request is in progress")

type replayRecord struct {
	Fingerprint string      `json:"fingerprint"`
	RequestID   string      `json:"request_id"`
	State       string      `json:"state"`
	Status      int         `json:"status,omitempty"`
	Headers     http.Header `json:"headers,omitempty"`
	Body        []byte      `json:"body,omitempty"`
}

// Exactly one handler runs per credential/endpoint/key while its record is
// retained. A crash leaves an indeterminate marker, never permission to rerun
// an unknown operation. The caller must reconcile acknowledged work first.
func (api *APIServer) withInference(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := uuid.NewString()
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID, Idempotency-Replayed, Retry-After")
		ctx, cancel := context.WithCancel(withRecoveryBudget(r.Context(), id))
		defer cancel()
		r = r.WithContext(ctx)
		if r.Method != http.MethodPost || r.Header.Get("Idempotency-Key") == "" {
			next(w, r)
			return
		}
		api.handleReplay(w, r, next, cancel, id)
	}
}

func (api *APIServer) replayInput(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get("Idempotency-Key")
	if len(key) > 128 || strings.IndexFunc(key, func(c rune) bool { return c < 33 || c > 126 }) >= 0 {
		api.sendErrorCode(w, 400, "invalid_idempotency_key", "Idempotency-Key must contain 1-128 visible ASCII characters.")
		return "", false
	}
	if api.continuity == nil || !api.continuity.retain {
		api.sendErrorCode(w, 400, "idempotency_storage_disabled", "Idempotency-Key requires enabled continuity storage.")
		return "", false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, requestBodyMax))
	if err != nil {
		api.sendRequestBodyError(w, err)
		return "", false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	fingerprint, err := replayFingerprint(r, body)
	if err != nil {
		api.sendErrorCode(w, 400, "invalid_idempotency_request", err.Error())
		return "", false
	}
	return fingerprint, true
}

func replayFingerprint(r *http.Request, body []byte) (string, error) {
	var data map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&data) != nil || data == nil {
		return "", errors.New("idempotent requests must contain a JSON object")
	}
	if data["store"] == false {
		return "", errors.New("Idempotency-Key retains the response; omit the key when using store=false")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return "", errors.New("request contains trailing JSON data")
	}
	model, _ := data["model"].(string)
	_, suffix := parseModelSessionID(model)
	session, _ := data["session_id"].(string)
	canonical, _ := json.Marshal(data)
	sid := resolveSessionID(r, sessionSources{ModelSuffix: suffix, BodySessionID: session})
	digest := sha256.Sum256([]byte(r.URL.Path + "\x00" + r.URL.RawQuery + "\x00" + sid + "\x00" + string(canonical)))
	return hex.EncodeToString(digest[:]), nil
}

func (s *continuityStore) replayLock(ctx context.Context, owner, key string) (*flock.Flock, error) {
	dir := filepath.Join(s.dir, "replay-locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	// A fixed number of lanes bounds lock-file growth. Locks are never removed
	// while another process may still hold an open handle to the same inode.
	sum := sha256.Sum256([]byte(owner + "\x00" + key))
	lock := flock.New(filepath.Join(dir, fmt.Sprintf("%02d.lock", sum[0]%64)))
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ok, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil || !ok {
		_ = lock.Close()
		if err != nil && ctx.Err() == nil {
			return nil, err
		}
		return nil, errReplayBusy
	}
	return lock, nil
}

func (s *continuityStore) loadReplay(ctx context.Context, owner, key string) (replayRecord, error) {
	var record replayRecord
	err := s.locked(ctx, func(aead cipher.AEAD) error { return s.read(aead, "replay", owner, key, &record) })
	return record, err
}

func (s *continuityStore) saveReplay(ctx context.Context, owner, key string, record replayRecord) error {
	return s.locked(ctx, func(aead cipher.AEAD) error { return s.write(aead, "replay", owner, key, record) })
}

func (api *APIServer) handleReplay(w http.ResponseWriter, r *http.Request, next http.HandlerFunc, cancel context.CancelFunc, id string) {
	fingerprint, ok := api.replayInput(w, r)
	if !ok {
		return
	}
	owner, key := api.callerScope(r), r.URL.Path+"\x00"+r.Header.Get("Idempotency-Key")
	lock, err := api.continuity.replayLock(r.Context(), owner, key)
	if err != nil {
		if !errors.Is(err, errReplayBusy) {
			api.sendContinuityError(w, err)
			return
		}
		w.Header().Set("Retry-After", "1")
		api.sendErrorCode(w, 409, "idempotency_in_progress", "A request on this idempotency lane is still running; retry the same key shortly.")
		return
	}
	defer lock.Close()
	record, err := api.continuity.loadReplay(r.Context(), owner, key)
	if !errors.Is(err, errStateNotFound) {
		api.answerReplay(w, record, fingerprint, err)
		return
	}
	record = replayRecord{Fingerprint: fingerprint, RequestID: id, State: "running"}
	if err := api.continuity.saveReplay(r.Context(), owner, key, record); err != nil {
		api.sendContinuityError(w, err)
		return
	}
	api.captureReplay(w, r, next, cancel, owner, key, record)
}

func (api *APIServer) answerReplay(w http.ResponseWriter, record replayRecord, fingerprint string, err error) {
	if err != nil {
		api.sendContinuityError(w, err)
		return
	}
	if record.Fingerprint != fingerprint {
		api.sendErrorCode(w, 409, "idempotency_conflict", "This Idempotency-Key was already used with a different request or session.")
		return
	}
	w.Header().Set("X-Request-ID", record.RequestID)
	if record.State != "complete" {
		api.sendErrorCode(w, 409, "idempotency_indeterminate", "The earlier request was interrupted or its outcome is unknown. It was not rerun. Reconcile acknowledged tool results and resume with a new key.")
		return
	}
	maps.Copy(w.Header(), record.Headers)
	w.Header().Set("Idempotency-Replayed", "true")
	w.WriteHeader(record.Status)
	_, _ = w.Write(record.Body)
}

func (api *APIServer) captureReplay(w http.ResponseWriter, r *http.Request, next http.HandlerFunc, cancel context.CancelFunc, owner, key string, record replayRecord) {
	capture := &replayWriter{ResponseWriter: w, cancel: cancel}
	completed := false
	defer func() {
		api.finishReplay(r, capture, owner, key, record, completed)
	}()
	wrapped := http.ResponseWriter(capture)
	if flusher, ok := w.(http.Flusher); ok {
		wrapped = &flushingReplayWriter{replayWriter: capture, flusher: flusher}
	}
	next(wrapped, r)
	completed = true
}

func (api *APIServer) finishReplay(r *http.Request, capture *replayWriter, owner, key string, record replayRecord, completed bool) {
	if completed && r.Context().Err() == nil && capture.err == nil && capture.body.Len() > 0 {
		record.State, record.Status, record.Body = "complete", capture.status, capture.body.Bytes()
		record.Headers = replayHeaders(capture.Header())
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if err := api.continuity.saveReplay(ctx, owner, key, record); err != nil {
		logging.Errorf("idempotency receipt unavailable: request_id=%s", record.RequestID)
	}
	if errors.Is(capture.err, errReplayCapacity) {
		api.replayCapacityError(capture, r.URL.Path)
	}
}

func replayHeaders(source http.Header) http.Header {
	result := http.Header{}
	for _, key := range []string{"Content-Type", "Cache-Control", "Access-Control-Allow-Origin", "X-Request-ID", "Retry-After"} {
		if value := source.Get(key); value != "" {
			result.Set(key, value)
		}
	}
	return result
}
