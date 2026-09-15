package servers

import (
	"context"
	"crypto/cipher"
	"encoding/json"
	"errors"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/payload"
	"github.com/KilimcininKorOglu/M365Bridge/pkg/toolcalling"
)

type checkpointUpdate struct {
	initial          toolcalling.TaskState
	calls            []toolcalling.LedgerCall
	results          []toolcalling.LedgerResult
	discardedCalls   []toolcalling.LedgerCall
	discardedResults []toolcalling.LedgerResult
	cancellationID   string
}

// A cancellation is an event, not a permanent mode. Remember it once, retire
// acknowledged older plans, and allow later user work to progress normally.
func checkpointUpdateFromMessages(messages []payload.Message) checkpointUpdate {
	var update checkpointUpdate
	start := taskCancellationIndex(messages) + 1
	if start > 0 {
		var users []string
		for _, message := range messages[:start] {
			if message.Role == "user" && len(message.ToolResults) == 0 && !message.ToolProgress {
				users = append(users, message.Content)
			}
		}
		encoded, _ := json.Marshal(users)
		update.cancellationID = statePathKey("cancel", "", string(encoded))
		update.discardedCalls, update.discardedResults, _ = messageToolHistory(messages[:start])
	}
	scoped, initial := historyAfterCheckpoint(messages[start:])
	update.initial = initial
	update.calls, update.results, _ = messageToolHistory(scoped)
	return update
}

func (c *taskCheckpoint) remember(id string) error {
	if c.Seen[id] {
		return nil
	}
	if len(c.Seen) >= checkpointEventsMax {
		return errStateTooLarge
	}
	c.Seen[id] = true
	return nil
}

func (c *taskCheckpoint) applyCalls(calls []toolcalling.LedgerCall, results []toolcalling.LedgerResult, discard bool) error {
	byID := map[string]toolcalling.LedgerResult{}
	for _, result := range results {
		byID[result.ID] = result
	}
	for _, call := range calls {
		if c.Seen[call.ID] {
			continue
		}
		next, valid := toolcalling.ApplyAcknowledgedPlan(c.Tasks, call, byID[call.ID])
		if !valid {
			continue
		}
		if err := c.remember(call.ID); err != nil {
			return err
		}
		if !discard {
			c.Tasks = next
		}
	}
	return nil
}

func (c *taskCheckpoint) apply(update checkpointUpdate) error {
	if c.Seen == nil {
		c.Seen = map[string]bool{}
	}
	if update.cancellationID != "" && !c.Seen[update.cancellationID] {
		if err := c.remember(update.cancellationID); err != nil {
			return err
		}
		c.Tasks = toolcalling.TaskState{}
	}
	if err := c.applyCalls(update.discardedCalls, update.discardedResults, true); err != nil {
		return err
	}
	return c.applyCalls(update.calls, update.results, false)
}

func (s *continuityStore) checkpoint(ctx context.Context, owner, sid string, update checkpointUpdate) (toolcalling.TaskState, error) {
	var checkpoint taskCheckpoint
	err := s.locked(ctx, func(aead cipher.AEAD) error {
		if err := s.read(aead, "checkpoint", owner, sid, &checkpoint); err != nil {
			if !errors.Is(err, errStateNotFound) {
				return err
			}
			checkpoint.Tasks = update.initial
		}
		if err := checkpoint.apply(update); err != nil {
			return err
		}
		if len(checkpoint.Seen) == 0 && !checkpoint.Tasks.Unfinished() {
			return nil
		}
		return s.write(aead, "checkpoint", owner, sid, checkpoint)
	})
	return checkpoint.Tasks, err
}
