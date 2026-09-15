package servers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/toolcalling"
)

func TestResponseHistorySurvivesRestartAndIsCredentialScoped(t *testing.T) {
	root := t.TempDir()
	store := newContinuityStore(root)
	ctx := context.Background()
	first := storedResponse{ID: "resp-first", SessionID: "board-a", Input: []any{map[string]any{"role": "user", "content": "private board objective"}}, Output: []any{map[string]any{"role": "assistant", "content": "first answer"}}}
	if err := store.saveResponse(ctx, "owner-a", first); err != nil {
		t.Fatal(err)
	}
	if err := store.saveResponse(ctx, "owner-a", storedResponse{ID: "resp-second", ParentID: first.ID, SessionID: first.SessionID, Input: []any{map[string]any{"role": "user", "content": "next"}}}); err != nil {
		t.Fatal(err)
	}
	store = newContinuityStore(root)
	history, sid, err := store.history(ctx, "owner-a", "resp-second")
	if err != nil || sid != "board-a" || len(history) != 3 {
		t.Fatalf("history was not restored: %v, %s, %d", err, sid, len(history))
	}
	if _, _, err := store.history(ctx, "owner-b", "resp-first"); !errors.Is(err, errStateNotFound) {
		t.Fatal("another credential accessed stored history")
	}
	data, err := os.ReadFile(filepath.Join(root, statePathKey("response", "owner-a", "resp-first")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private board objective") {
		t.Fatal("response content was stored in plaintext")
	}
}

func TestCompactionCapsuleRejectsTamperingOtherOwnersAndExpiry(t *testing.T) {
	store := newContinuityStore(t.TempDir())
	ctx := context.Background()
	token, err := store.encodeCompaction(ctx, "owner-a", compactState{SessionID: "board-a", Summary: "private summary", Tasks: toolcalling.TaskState{Pending: []string{"implement WIP limits"}}})
	if err != nil {
		t.Fatal(err)
	}
	restarted := newContinuityStore(store.dir)
	state, err := restarted.decodeCompaction(ctx, "owner-a", token)
	if err != nil || !state.Tasks.Unfinished() {
		t.Fatal("restart lost compacted tasks", err)
	}
	if _, err := restarted.decodeCompaction(ctx, "owner-b", token); err == nil {
		t.Fatal("another owner opened compaction state")
	}
	tampered := token[:len(token)/2] + "!" + token[len(token)/2+1:]
	if _, err := restarted.decodeCompaction(ctx, "owner-a", tampered); err == nil {
		t.Fatal("tampered compaction accepted")
	}
	restarted.now = func() time.Time { return time.Now().Add(continuityTTL + time.Minute) }
	if _, err := restarted.decodeCompaction(ctx, "owner-a", token); err == nil {
		t.Fatal("expired compaction accepted")
	}
}

func TestCheckpointMergesConcurrentAcknowledgementsAndCancellation(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	var workers sync.WaitGroup
	for _, id := range []string{"board", "cards", "limits"} {
		workers.Go(func() {
			store := newContinuityStore(root)
			call := toolcalling.LedgerCall{ID: id, Name: "update_plan", Arguments: `{"plan":[{"step":"` + id + `","status":"pending"}]}`}
			_, err := store.checkpoint(ctx, "owner", "session", checkpointUpdate{calls: []toolcalling.LedgerCall{call}, results: []toolcalling.LedgerResult{{ID: id, Content: "updated"}}})
			if err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	store := newContinuityStore(root)
	state, err := store.checkpoint(ctx, "owner", "session", checkpointUpdate{})
	if err != nil || len(state.Pending) != 3 {
		t.Fatalf("concurrent tasks were lost: %#v %v", state, err)
	}
	if _, err := store.checkpoint(ctx, "owner", "session", checkpointUpdate{cancellationID: "cancel-event"}); err != nil {
		t.Fatal(err)
	}
	state, err = store.checkpoint(ctx, "owner", "session", checkpointUpdate{})
	if err != nil || state.Unfinished() {
		t.Fatal("cancelled checkpoint reopened", err)
	}
}

func TestCorruptCheckpointFailsClosed(t *testing.T) {
	store := newContinuityStore(t.TempDir())
	ctx := context.Background()
	_, err := store.checkpoint(ctx, "owner", "session", checkpointUpdate{initial: toolcalling.TaskState{Pending: []string{"pending work"}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.dir, statePathKey("checkpoint", "owner", "session"))
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.checkpoint(ctx, "owner", "session", checkpointUpdate{}); err == nil {
		t.Fatal("corrupt state was silently replaced with an empty plan")
	}
}

func TestRetentionCapacityDoesNotEvictLiveTaskCheckpoints(t *testing.T) {
	store := newContinuityStore(t.TempDir())
	ctx := context.Background()
	update := checkpointUpdate{initial: toolcalling.TaskState{Pending: []string{"verify the board"}}}
	for index := range continuityRecordsMax {
		if _, err := store.checkpoint(ctx, "owner", fmt.Sprint(index), update); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.saveResponse(ctx, "owner", storedResponse{ID: "over-capacity"}); !errors.Is(err, errStateTooLarge) {
		t.Fatalf("capacity error = %v", err)
	}
	state, err := newContinuityStore(store.dir).checkpoint(ctx, "owner", "0", checkpointUpdate{})
	if err != nil || !state.Unfinished() {
		t.Fatal("capacity pressure lost an unfinished queue", err)
	}
}

func TestResponseExpiryAndOversizedRecordsFailExplicitly(t *testing.T) {
	store := newContinuityStore(t.TempDir())
	ctx := context.Background()
	if err := store.saveResponse(ctx, "owner", storedResponse{ID: "expires"}); err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.Now().Add(continuityTTL + time.Minute) }
	if _, err := store.response(ctx, "owner", "expires"); !errors.Is(err, errStateNotFound) {
		t.Fatalf("expired response = %v", err)
	}
	oversized := storedResponse{ID: "too-large", Input: []any{strings.Repeat("x", continuityRecordMax+1)}}
	if err := store.saveResponse(ctx, "owner", oversized); !errors.Is(err, errStateTooLarge) {
		t.Fatalf("oversized response = %v", err)
	}
	if _, err := store.response(ctx, "owner", oversized.ID); !errors.Is(err, errStateNotFound) {
		t.Fatal("oversized response was partially retained")
	}
}
