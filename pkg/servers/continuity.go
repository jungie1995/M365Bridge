package servers

import (
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/models"
	"github.com/KilimcininKorOglu/M365Bridge/pkg/payload"
	"github.com/KilimcininKorOglu/M365Bridge/pkg/toolcalling"
)

type localToolEvidence struct {
	calls   []toolcalling.LedgerCall
	results []toolcalling.LedgerResult
	tasks   toolcalling.TaskState
}

func (p *localToolEvidence) record(call toolcalling.ToolCall, result string) {
	id := fmt.Sprintf("local-%d", len(p.calls))
	p.calls = append(p.calls, toolcalling.LedgerCall{ID: id, Name: call.Name, Arguments: string(call.Arguments)})
	p.results = append(p.results, toolcalling.LedgerResult{ID: id, Content: result})
}

func (p *localToolEvidence) ledger(rounds int) toolcalling.Ledger {
	ledger := toolcalling.BuildLedger(p.calls, p.results, rounds)
	ledger.Tasks = p.tasks
	return ledger
}

func continuationRepairNote(sim toolcalling.SimulatedResult, messages []payload.Message, tools []toolcalling.ToolDef, contracts toolcalling.ToolContracts, rawText string) (string, bool, bool) {
	tasks := buildToolLedger(messages).Tasks
	if tasks.Unfinished() && !toolcalling.TaskBlocked(sim.Content) {
		return toolcalling.TaskContinuityInstruction + "\n" + tasks.Note(), false, true
	}
	return repairNote(sim, tools, contracts, rawText)
}

func (api *APIServer) checkedSimulation(provider toolLoopProvider, messages []payload.Message, cfg models.ModelConfig, tools []toolcalling.ToolDef, choice string, noParallel bool, text string) (toolcalling.SimulatedResult, error) {
	contracts := toolcalling.ContractsFor(tools).WithChoice(choice).WithoutParallel(noParallel)
	sim := parseLoopSimulation(provider, text, tools, contracts)
	sim = api.repairSimulatedToolCalls(provider, messages, cfg, tools, contracts, text, sim)
	return guardToolProgress(buildToolLedger(messages), choice, sim)
}

func isProgressFailure(err error) bool {
	return errors.Is(err, toolcalling.ErrTaskIncomplete) || errors.Is(err, toolcalling.ErrToolLoopStalled)
}

func retryableSimulationFailure(err error) bool {
	return errors.Is(err, errSimulatedToolCallRequired) || isProgressFailure(err)
}

func messageToolHistory(messages []payload.Message) ([]toolcalling.LedgerCall, []toolcalling.LedgerResult, int) {
	var calls []toolcalling.LedgerCall
	var results []toolcalling.LedgerResult
	rounds := 0
	for _, message := range messages {
		if len(message.ToolCalls) > 0 {
			rounds++
		}
		for _, call := range message.ToolCalls {
			calls = append(calls, toolcalling.LedgerCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
		}
		for _, result := range message.ToolResults {
			results = append(results, toolcalling.LedgerResult{ID: result.ID, Content: result.Content, IsError: result.IsError})
		}
	}
	return calls, results, rounds
}

func latestUserCancelsTasks(messages []payload.Message) bool {
	for _, message := range slices.Backward(messages) {
		if message.Role == "user" && len(message.ToolResults) == 0 && !message.ToolProgress {
			return message.TaskCancelled || toolcalling.TaskCancellation(message.Content)
		}
	}
	return false
}

func taskHistorySinceCancellation(messages []payload.Message) []payload.Message {
	start := 0
	for index, message := range messages {
		if message.Role == "user" && len(message.ToolResults) == 0 && !message.ToolProgress &&
			(message.TaskCancelled || toolcalling.TaskCancellation(message.Content)) {
			start = index + 1
		}
	}
	return messages[start:]
}

// guardToolProgress never synthesizes a successful completion from blocked
// calls. The same guard serves OpenAI, Anthropic and Responses requests.
func guardToolProgress(ledger toolcalling.Ledger, toolChoice string, sim toolcalling.SimulatedResult) (toolcalling.SimulatedResult, error) {
	if toolChoice == "none" || toolChoiceForcesACall(toolChoice) {
		return sim, nil
	}
	if len(sim.ToolCalls) == 0 {
		if ledger.Tasks.Unfinished() && !toolcalling.TaskBlocked(sim.Content) {
			return sim, toolcalling.ErrTaskIncomplete
		}
		return sim, nil
	}
	kept, dropped := ledger.FilterRepeated(sim.ToolCalls)
	if len(dropped) > 0 && len(kept) == 0 {
		return sim, toolcalling.ErrToolLoopStalled
	}
	// A new operation in the same batch may make the old observation stale.
	// Preserve that batch; the next request will carry its actual results.
	return sim, nil
}

func (api *APIServer) sendAnthropicProgressError(w http.ResponseWriter, err error) {
	_, code, message := streamErrorFields("tool execution", err)
	api.sendAnthropicSSE(w, "error", map[string]any{
		"type": "error", "error": map[string]any{"type": code, "message": message},
	})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}
