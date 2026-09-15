package servers

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/models"
	"github.com/KilimcininKorOglu/M365Bridge/pkg/toolcalling"
)

// Exercise upstream's new shared handlers, not just the standalone guard.
func TestRefactoredHandlersPropagateStalledLoops(t *testing.T) {
	api := &APIServer{}
	tools := []toolcalling.ToolDef{{Type: "function", Name: "run_tests", Function: toolcalling.ToolDefFunc{Name: "run_tests"}}}
	var history strings.Builder
	history.WriteString(`[{"role":"user","content":"fix and test"}`)
	for i := range toolcalling.MaxUnchangedToolResults {
		id := string(rune('a' + i))
		history.WriteString(`,{"role":"assistant","tool_calls":[{"id":"` + id + `","type":"function","function":{"name":"run_tests","arguments":"{}"}}]}`)
		history.WriteString(`,{"role":"tool","tool_call_id":"` + id + `","content":"unchanged failure"}`)
	}
	history.WriteString("]")
	messages := decodeMessages(t, history.String())
	openAI := `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"new","type":"function","function":{"name":"run_tests","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
	anthropic := `{"content":[{"type":"tool_use","id":"new","name":"run_tests","input":{}}],"stop_reason":"tool_use"}`
	cases := map[string]func() error{
		"chat stream": func() error {
			s := &chatStream{}
			s.fullText.WriteString(openAI)
			_, _, err := api.chatToolCalls(s, messages, models.ModelConfig{}, tools, "auto", nil, true, false)
			return err
		},
		"completion stream": func() error {
			s := &completionStream{}
			s.fullText.WriteString(openAI)
			_, _, err := api.completionToolCalls(s, messages, models.ModelConfig{}, tools, "auto", nil, true)
			return err
		},
		"Anthropic stream": func() error {
			s := &anthropicStream{}
			s.fullText.WriteString(anthropic)
			_, _, _, err := api.anthropicToolCalls(s, messages, models.ModelConfig{}, tools, "auto", nil, true, false, "")
			return err
		},
		"buffered chat": func() error {
			_, _, _, err := api.applySimulatedToolCalls(toolLoopOpenAI, messages, models.ModelConfig{}, tools, "auto", false, openAI, nil)
			return err
		},
		"buffered Anthropic": func() error {
			_, _, _, err := api.applySimulatedToolCalls(toolLoopAnthropic, messages, models.ModelConfig{}, tools, "auto", false, anthropic, nil)
			return err
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			if err := run(); !errors.Is(err, toolcalling.ErrToolLoopStalled) {
				t.Fatalf("guard was bypassed: %v", err)
			}
		})
	}
}

func TestRefactoredResponsesErrorKeepsProgressCode(t *testing.T) {
	for _, err := range []error{toolcalling.ErrTaskIncomplete, toolcalling.ErrToolLoopStalled} {
		rec := httptest.NewRecorder()
		writeResponsesParseError(rec, "test-model", err)
		if rec.Code != 409 || !strings.Contains(rec.Body.String(), err.Error()) {
			t.Fatalf("progress failure lost its code: %d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestRefactoredAnthropicDecoderPreservesPlanFailure(t *testing.T) {
	messages := decodeMessages(t, `[
	{"role":"user","content":"fix the bug"},
	{"role":"assistant","content":[{"type":"tool_use","id":"plan","name":"update_plan","input":{"plan":[{"step":"fix","status":"pending"}]}}]},
	{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan","content":"updated"}]},
	{"role":"assistant","content":[{"type":"tool_use","id":"done","name":"update_plan","input":{"plan":[{"step":"fix","status":"completed"}]}}]},
	{"role":"user","content":[{"type":"tool_result","tool_use_id":"done","content":"denied","is_error":true}]}
	]`)
	if !buildToolLedger(messages).Tasks.Unfinished() {
		t.Fatal("failed plan update silently completed work")
	}
}
