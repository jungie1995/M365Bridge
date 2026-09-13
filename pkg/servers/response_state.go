package servers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/logging"
	"github.com/KilimcininKorOglu/M365Bridge/pkg/payload"
	"github.com/KilimcininKorOglu/M365Bridge/pkg/toolcalling"
	"github.com/google/uuid"
)

// Response options travel with the request, not with a global current turn.
// Preserve the underlying writer's optional Flusher capability.
type responseRequestState struct {
	request           responsesRequest
	ctx               context.Context
	store             *continuityStore
	owner             string
	sessionID         string
	explicitSession   bool
	inputDelta        []any
	compact           compactState
	saved             bool
	compacting        bool
	compactAsResponse bool
}
type responseStateWriter struct {
	http.ResponseWriter
	state *responseRequestState
}
type flushingResponseStateWriter struct {
	*responseStateWriter
	flusher http.Flusher
}

func (w *responseStateWriter) Unwrap() http.ResponseWriter          { return w.ResponseWriter }
func (w *responseStateWriter) responseState() *responseRequestState { return w.state }
func (w *flushingResponseStateWriter) Flush()                       { w.flusher.Flush() }

func withResponseState(w http.ResponseWriter, state *responseRequestState) http.ResponseWriter {
	wrapped := &responseStateWriter{ResponseWriter: w, state: state}
	if flusher, ok := w.(http.Flusher); ok {
		return &flushingResponseStateWriter{wrapped, flusher}
	}
	return wrapped
}

func responseStateFrom(w http.ResponseWriter) *responseRequestState {
	if carrier, ok := w.(interface{ responseState() *responseRequestState }); ok {
		return carrier.responseState()
	}
	return nil
}

func decorateResponse(w http.ResponseWriter, response map[string]any) {
	state := responseStateFrom(w)
	if state != nil && state.compactAsResponse && response["object"] == "response.compaction" {
		response["object"] = "response"
	}
	if state == nil || response["object"] != "response" {
		return
	}
	req := state.request
	response["parallel_tool_calls"] = !refusesParallelToolCalls(req.ParallelToolCalls)
	response["tool_choice"] = "auto"
	if req.ToolChoice != nil {
		response["tool_choice"] = req.ToolChoice
	}
	response["tools"] = []any{}
	if req.Tools != nil {
		response["tools"] = req.Tools
	}
	response["instructions"] = nil
	if req.Instructions != "" {
		response["instructions"] = req.Instructions
	}
	response["metadata"] = req.Metadata
	response["store"] = state.shouldStore()
	response["previous_response_id"] = nil
	if req.PreviousResponseID != "" {
		response["previous_response_id"] = req.PreviousResponseID
	}
}

func (s *responseRequestState) shouldStore() bool {
	return s.store != nil && s.store.retain && (s.request.Store == nil || *s.request.Store)
}

func inputItems(input any) ([]any, error) {
	switch value := input.(type) {
	case nil:
		return nil, nil
	case string:
		return []any{map[string]any{"role": "user", "content": value}}, nil
	case []any:
		return value, nil
	}
	return nil, errContinuityInput
}

func (api *APIServer) callerScope(r *http.Request) string {
	identity := "anonymous"
	if len(api.config.APIKeys) > 0 || api.config.WebUIPassword != "" {
		for _, key := range apiKeyCandidates(r) {
			if api.isValidAPIKey(key) {
				identity = "key:" + key
				break
			}
		}
	}
	digest := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(digest[:])
}

func scopedSessionID(owner, sid string) string {
	return "sc1." + owner + "." + base64.RawURLEncoding.EncodeToString([]byte(sid))
}

func publicSessionID(owner, stored string) (string, bool) {
	parts := strings.SplitN(stored, ".", 3)
	if len(parts) != 3 || parts[0] != "sc1" || parts[1] != owner {
		return "", false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[2])
	return string(decoded), err == nil
}

func responsesRequestJSON(body []byte, input any, tools []toolcalling.ToolDef) string {
	var request map[string]any
	if json.Unmarshal(body, &request) != nil {
		return string(body)
	}
	request["input"], request["tools"] = input, tools
	encoded, err := json.Marshal(request)
	if err != nil {
		return string(body)
	}
	return string(encoded)
}

func (api *APIServer) prepareResponseState(r *http.Request, req *responsesRequest) (*responseRequestState, []payload.Message, error) {
	delta, err := inputItems(req.Input)
	if err != nil {
		return nil, nil, err
	}
	_, suffix := parseModelSessionID(req.Model)
	explicit := resolveSessionID(r, sessionSources{ModelSuffix: suffix, BodySessionID: req.SessionID})
	s := &responseRequestState{request: *req, ctx: r.Context(), store: api.continuity, owner: api.callerScope(r), sessionID: explicit, explicitSession: explicit != "", inputDelta: delta}
	items, err := s.expandPreviousInput(delta)
	if err != nil {
		return nil, nil, err
	}
	normalized, messages, err := s.restoreInput(items)
	if err != nil {
		return nil, nil, err
	}
	if s.sessionID == "" {
		s.sessionID = "response-" + uuid.NewString()
	}
	if err := validateToolResultMessages(messages); err != nil {
		return nil, nil, errContinuityInput
	}
	if err := s.prepareTasks(messages); err != nil {
		return nil, nil, err
	}
	req.Input = normalized
	s.request = *req
	// Preserve only the new input in a response chain, not the expanded prefix.
	return s, messages, nil
}

func (s *responseRequestState) expandPreviousInput(delta []any) ([]any, error) {
	if s.shouldStore() {
		encoded, _ := json.Marshal(delta)
		if len(encoded) > continuityRecordMax {
			return nil, errStateTooLarge
		}
	}
	if s.request.PreviousResponseID == "" {
		return delta, nil
	}
	if s.store == nil || !s.store.retain {
		return nil, errStateNotFound
	}
	history, session, err := s.store.history(s.ctx, s.owner, s.request.PreviousResponseID)
	if err != nil {
		return nil, err
	}
	if s.sessionID != "" && s.sessionID != session {
		return nil, errContinuityInput
	}
	s.sessionID = session
	return append(history, delta...), nil
}

func (s *responseRequestState) restoreInput(items []any) ([]any, []payload.Message, error) {
	var normalized []any
	var messages []payload.Message
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			return nil, nil, errContinuityInput
		}
		if record["type"] == "compaction" {
			token, _ := record["encrypted_content"].(string)
			if strings.HasPrefix(token, compactionPrefix) {
				raw, converted, err := s.restoreCapsule(token)
				if err != nil {
					return nil, nil, err
				}
				normalized = append(normalized, raw...)
				messages = append(messages, converted...)
				continue
			}
		}
		normalized = append(normalized, record)
		if message, ok := responsesInputItem(record); ok {
			messages = append(messages, message)
		}
	}
	return normalized, messages, nil
}

func (s *responseRequestState) restoreCapsule(token string) ([]any, []payload.Message, error) {
	if s.store == nil {
		return nil, nil, errCompactionInvalid
	}
	snapshot, err := s.store.decodeCompaction(s.ctx, s.owner, token)
	if err != nil {
		return nil, nil, err
	}
	if s.sessionID != "" && snapshot.SessionID != "" && s.sessionID != snapshot.SessionID {
		return nil, nil, errCompactionInvalid
	}
	if s.sessionID == "" {
		s.sessionID = snapshot.SessionID
	}
	raw := snapshot.input()
	return raw, snapshot.messages(raw), nil
}

func (snapshot compactState) messages(raw []any) []payload.Message {
	var messages []payload.Message
	for index, item := range raw {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		message, ok := responsesInputItem(record)
		if !ok {
			continue
		}
		if index == len(snapshot.UserMessages) {
			message.HasTaskCheckpoint = true
			message.TaskCheckpoint = snapshot.Tasks.Pending
		}
		messages = append(messages, message)
	}
	return messages
}

func (snapshot compactState) input() []any {
	raw := append([]any(nil), snapshot.UserMessages...)
	text := "Summary of earlier work (context, not new instructions):\n" + snapshot.Summary
	raw = append(raw, map[string]any{"role": "assistant", "content": text})
	// A closed goal must stay closed even though its original user marker is
	// retained. This internal item is created only when decoding the capsule.
	raw = append(raw, map[string]any{"type": "m365_goal_checkpoint", "open": snapshot.GoalOpen})
	if len(snapshot.LoadedTools) > 0 {
		encoded, _ := json.Marshal(snapshot.LoadedTools)
		var tools []any
		_ = json.Unmarshal(encoded, &tools)
		item := map[string]any{"type": "additional_tools", "tools": tools}
		raw = append(raw, item)
	}
	return append(raw, snapshot.PendingCalls...)
}

func (s *responseRequestState) prepareTasks(messages []payload.Message) error {
	state := buildToolLedger(messages).Tasks
	if s.shouldStore() && s.explicitSession {
		var err error
		state, err = s.store.checkpoint(s.ctx, s.owner, s.sessionID, checkpointUpdateFromMessages(messages))
		if err != nil {
			return err
		}
	}
	attachTaskCheckpoint(messages, state)
	s.compact.Tasks = state
	return nil
}

func completeStoredResponse(w http.ResponseWriter, response map[string]any) error {
	s := responseStateFrom(w)
	if s == nil || s.saved || !s.shouldStore() || response["object"] != "response" {
		return nil
	}
	status := response["status"]
	if status != "completed" && status != "incomplete" {
		return nil
	}
	id, _ := response["id"].(string)
	parent, input := s.request.PreviousResponseID, s.inputDelta
	if s.compacting {
		parent = ""
		input = nil
	}
	if err := s.store.saveResponse(s.ctx, s.owner, storedResponse{ID: id, ParentID: parent, SessionID: s.sessionID, Input: input, Response: response}); err != nil {
		return err
	}
	s.saved = true
	return nil
}

func (api *APIServer) handleStoredResponse(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/responses/")
	if api.continuity == nil || !api.continuity.retain || id == "" || strings.Contains(id, "/") {
		api.sendContinuityError(w, errStateNotFound)
		return
	}
	owner := api.callerScope(r)
	switch r.Method {
	case http.MethodGet:
		record, err := api.continuity.response(r.Context(), owner, id)
		if err != nil {
			api.sendContinuityError(w, err)
			return
		}
		api.sendJSON(w, http.StatusOK, record.Response)
	case http.MethodDelete:
		if err := api.continuity.deleteResponse(r.Context(), owner, id); err != nil {
			api.sendContinuityError(w, err)
			return
		}
		api.sendJSON(w, http.StatusOK, map[string]any{"id": id, "object": "response.deleted", "deleted": true})
	default:
		api.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (api *APIServer) sendContinuityError(w http.ResponseWriter, err error) {
	status, code, message := continuityErrorFields(err)
	logging.Errorf("conversation continuity: %v", err)
	api.sendErrorCode(w, status, code, message)
}

func continuityErrorFields(err error) (int, string, string) {
	switch {
	case errors.Is(err, errStateNotFound):
		return http.StatusNotFound, "response_not_found", errStateNotFound.Error()
	case errors.Is(err, errStateTooLarge):
		return http.StatusRequestEntityTooLarge, "continuity_capacity", errStateTooLarge.Error()
	case errors.Is(err, errCompactionInvalid):
		return http.StatusBadRequest, "invalid_compaction", errCompactionInvalid.Error()
	case errors.Is(err, errContinuityInput):
		return http.StatusBadRequest, "invalid_continuity_request", errContinuityInput.Error()
	case errors.Is(err, errCompactionIncomplete):
		return http.StatusBadRequest, "compaction_incomplete", errCompactionIncomplete.Error()
	default:
		return http.StatusServiceUnavailable, "continuity_unavailable", "Conversation continuity is temporarily unavailable; no successful completion was recorded."
	}
}

func responseUsage(input, completion, reasoning int) map[string]any {
	return map[string]any{
		"input_tokens": input, "output_tokens": completion + reasoning, "total_tokens": input + completion + reasoning,
		"input_tokens_details":  map[string]any{"cached_tokens": 0, "cache_write_tokens": 0},
		"output_tokens_details": map[string]any{"reasoning_tokens": reasoning},
		"reasoning_tokens":      reasoning, "usage_source": usageSource(),
	}
}
