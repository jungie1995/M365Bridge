package servers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/payload"
)

func TestAllProtocolsSendTheCompleteParallelToolBatchOnReusedConversation(t *testing.T) {
	for name, inject := range map[string]func(*[]payload.Message, string, string, string){
		"chat": injectSimulatedPrompt, "anthropic": injectSimulatedPromptAnthropic, "responses": injectSimulatedPromptResponses,
	} {
		t.Run(name, func(t *testing.T) {
			messages := decodeMessages(t, `[
				{"role":"user","content":"Read all project files and implement the board."},
				{"role":"assistant","tool_calls":[
					{"id":"a","type":"function","function":{"name":"read","arguments":"{\"filePath\":\"service.py\"}"}},
					{"id":"b","type":"function","function":{"name":"read","arguments":"{\"filePath\":\"__init__.py\"}"}}]},
				{"role":"tool","tool_call_id":"a","content":"SERVICE_CONTENT_MUST_REACH_UPSTREAM"},
				{"role":"tool","tool_call_id":"b","content":"LAST_RESULT_EMPTY_INIT"}
			]`)
			before := buildToolLedger(messages)
			requestJSON := `{"messages":[{"role":"tool","content":"SERVICE_CONTENT_MUST_REACH_UPSTREAM"},{"role":"tool","content":"LAST_RESULT_EMPTY_INIT"}],"tools":[{"name":"read"}]}`
			inject(&messages, requestJSON, "auto", before.EvidenceNote())
			// includeHistory=false is the actual cached-conversation transport path.
			wire, err := payload.BuildConversationPayload("hex", "uuid", messages, false, "Precise", "", false, true, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, marker := range []string{"SERVICE_CONTENT_MUST_REACH_UPSTREAM", "LAST_RESULT_EMPTY_INIT", "tools"} {
				if !strings.Contains(wire, marker) {
					t.Fatalf("upstream lost %s", marker)
				}
			}
			after := buildToolLedger(messages)
			if after.Rounds != before.Rounds {
				t.Fatal("canonical transport changed client round evidence")
			}
			encoded, err := json.Marshal(messages)
			if err != nil || strings.Contains(string(encoded), "SimulationHistory") {
				t.Fatal("private ledger metadata escaped onto the wire")
			}
		})
	}
}
