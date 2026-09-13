package servers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/client"
	"github.com/KilimcininKorOglu/M365Bridge/pkg/models"
	"github.com/KilimcininKorOglu/M365Bridge/pkg/payload"
)

type contractBackend struct{ *client.M365Client }

func contractPrompt(messages []payload.Message) string {
	var prompt strings.Builder
	for _, message := range messages {
		prompt.WriteString(message.Content)
	}
	return prompt.String()
}

func contractTasksPresent(text string) bool {
	return strings.Contains(text, "implement board") && strings.Contains(text, "verify WIP")
}

func contractWantsTools(text string) bool {
	return strings.Contains(text, "SDK_TOOL") || strings.Contains(text, "SDK_CHECK_TASKS")
}

func contractReply(messages []payload.Message, hasTools bool) (string, error) {
	text := contractPrompt(messages)
	if strings.Contains(text, "SDK_FAILURE") {
		return "", errors.New("private upstream detail access_token=must-not-escape")
	}
	if strings.Contains(text, "SDK_CHECK_TASKS") && !contractTasksPresent(text) {
		return "CHECKPOINT_TASKS_MISSING", nil
	}
	if strings.Contains(text, "concise summary") {
		return "The board work is in progress.", nil
	}
	if strings.Contains(text, "SDK_RESULT") {
		return "CLIENT_RESULT_ACCEPTED", nil
	}
	if hasTools && contractWantsTools(text) {
		if strings.Contains(text, "Anthropic Messages API") {
			return `{"content":[{"type":"text","text":"Reading the board."},{"type":"tool_use","id":"server-repeated-id","name":"read_board","input":{"board":"alpha"}}],"stop_reason":"tool_use"}`, nil
		}
		return `{"choices":[{"message":{"role":"assistant","content":"Reading the board.","tool_calls":[{"id":"server-repeated-id","type":"function","function":{"name":"read_board","arguments":"{\"board\":\"alpha\"}"}}]},"finish_reason":"tool_calls"}]}`, nil
	}
	return "Board ready: café", nil
}

func (b *contractBackend) ChatConversation(m []payload.Message, a, c, d, e, f string, tools bool) (string, string, []client.ToolCall, string, string, error) {
	return b.ChatConversationContext(context.Background(), m, a, c, d, e, f, tools)
}
func (*contractBackend) ChatConversationContext(ctx context.Context, m []payload.Message, _, _, _, _, _ string, tools bool) (string, string, []client.ToolCall, string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", nil, "", "", err
	}
	text, err := contractReply(m, tools)
	return text, "", nil, "stop", "fixture-conversation", err
}
func (*contractBackend) ChatConversationStreamGenContext(ctx context.Context, m []payload.Message, _, _, _, _, _ string, tools bool) <-chan client.StreamChunk {
	out := make(chan client.StreamChunk)
	go func() {
		defer close(out)
		for _, chunk := range contractChunks(m, tools) {
			select {
			case out <- chunk:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func contractChunks(messages []payload.Message, tools bool) []client.StreamChunk {
	text, err := contractReply(messages, tools)
	if err != nil {
		return []client.StreamChunk{{Error: err}}
	}
	if strings.Contains(contractPrompt(messages), "SDK_DISCONNECT") {
		return []client.StreamChunk{{Text: "Partial answer"}}
	}
	late := strings.Contains(contractPrompt(messages), "SDK_LATE_REASONING")
	var chunks []client.StreamChunk
	runes := []rune(text)
	for len(runes) > 0 {
		n := min(17, len(runes))
		chunks = append(chunks, client.StreamChunk{Text: string(runes[:n])})
		runes = runes[n:]
		if late {
			chunks = append(chunks, client.StreamChunk{Thinking: "Brief public rationale."})
			late = false
		}
	}
	return append(chunks, client.StreamChunk{IsFinal: true, FinishReason: "stop", ConversationID: "fixture-conversation"})
}

func newContractServer(t *testing.T) (*APIServer, *httptest.Server) {
	t.Helper()
	root := t.TempDir()
	api := &APIServer{config: &models.Config{APIKeys: []string{"sdk-fixture-key", "sdk-other-key"}}, m365Client: &contractBackend{}, ctxCache: NewContextCache(filepath.Join(root, "cache")), continuity: newContinuityStore(filepath.Join(root, "continuity")), imageRefs: newImageRefStore()}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", api.withAuth(api.handleChatCompletions))
	mux.HandleFunc("/v1/messages", api.withAuth(api.handleAnthropicMessages))
	mux.HandleFunc("/v1/responses", api.withAuth(api.handleResponses))
	mux.HandleFunc("/v1/responses/compact", api.withAuth(api.handleResponsesCompact))
	mux.HandleFunc("/v1/responses/", api.withAuth(api.handleStoredResponse))
	mux.HandleFunc("/v1/models", api.withAuth(api.handleModels))
	mux.HandleFunc("/v1/sessions", api.withAuth(api.handleSessions))
	mux.HandleFunc("/v1/sessions/", api.withAuth(api.handleSession))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return api, server
}

func TestOfficialSDKContracts(t *testing.T) {
	python := os.Getenv("M365_SDK_PYTHON")
	if python == "" {
		t.Skip("set M365_SDK_PYTHON to a Python environment with tests/sdk-requirements.txt")
	}
	_, server := newContractServer(t)
	script, err := filepath.Abs("../../tests/sdk_contracts.py")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, script, "--base-url", server.URL+"/v1")
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatalf("official SDK contract checks failed: %v", err)
	}
}
