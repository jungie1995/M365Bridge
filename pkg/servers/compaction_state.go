package servers

import (
	"context"
	"net/http"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/payload"
)

func captureCompactState(input any, messages []payload.Message, session string) compactState {
	state := compactState{SessionID: session, Tasks: buildToolLedger(messages).Tasks, GoalOpen: responsesGoalContinuationOpen(input)}
	state.LoadedTools = mergeLoadedResponsesTools(input, nil)
	items, _ := inputItems(input)
	answered := answeredCompactCalls(items)
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if record["role"] == "user" {
			state.UserMessages = append(state.UserMessages, record)
		}
		id, _ := record["call_id"].(string)
		if id != "" && !answered[id] && isPendingCallType(record["type"]) {
			state.PendingCalls = append(state.PendingCalls, record)
		}
	}
	return state
}

func answeredCompactCalls(items []any) map[string]bool {
	answered := map[string]bool{}
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch record["type"] {
		case "function_call_output", "custom_tool_call_output", "tool_search_output":
			id, _ := record["call_id"].(string)
			answered[id] = true
		}
	}
	return answered
}

func isPendingCallType(kind any) bool {
	switch kind {
	case "function_call", "custom_tool_call", "tool_search_call":
		return true
	}
	return false
}

func sealCompactionSummary(w http.ResponseWriter, summary string) (string, error) {
	s := responseStateFrom(w)
	if s == nil || s.store == nil {
		return summary, nil
	}
	value := s.compact
	value.Summary = summary
	return s.store.encodeCompaction(s.ctx, s.owner, value)
}

func (api *APIServer) prepareSessionTasks(r *http.Request, sources sessionSources, messages []payload.Message, store *bool) error {
	if api.continuity == nil || !api.continuity.retain || (store != nil && !*store) {
		return nil
	}
	sources.BodyUser = "" // user identifies an account, not an isolated conversation.
	sources.PreviousResponseID = ""
	sid := resolveSessionID(r, sources)
	if sid == "" {
		return nil
	}
	if err := validateToolResultMessages(messages); err != nil {
		return err
	}
	s := responseRequestState{ctx: r.Context(), store: api.continuity, owner: api.callerScope(r), sessionID: sid, explicitSession: true, request: responsesRequest{Store: store}}
	return s.prepareTasks(messages)
}

func requestContext(contexts []context.Context) context.Context {
	if len(contexts) > 0 && contexts[0] != nil {
		return contexts[0]
	}
	return context.Background()
}
