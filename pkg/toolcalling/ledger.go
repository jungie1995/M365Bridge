package toolcalling

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/textcut"
)

// maxFailureSignature caps the normalized failure text kept for comparison.
// A stack trace can be arbitrarily long and only its head distinguishes one
// failure from another.
const maxFailureSignature = 500

// maxEvidenceResult caps the result text the ledger carries into the prompt. A
// build log or a test run is unbounded, and every completed call of the turn is
// restated on every request, so an uncapped result would grow the prompt until
// the turn no longer fits.
const maxEvidenceResult = 4000

// minEvidenceTail is the smallest tail kept when a result is compacted. The end
// of a command's output usually holds the verdict.
const minEvidenceTail = 80

// A few identical observations are normal during debugging. Only a sustained,
// consecutive run of identical operations AND complete results is stalled.
const MaxUnchangedToolResults = 6

var ErrToolLoopStalled = errors.New("tool_loop_stalled")

const ToolLoopStalledMessage = "The task is incomplete: repeated tool calls returned unchanged results without an intervening operation. Change the approach or resume with a new instruction; the bridge did not mark the work completed."

// failureSignal matches the wording a tool result uses to report that the tool
// did not do what it was asked. The ledger uses it to tell an answered call
// apart from an answered-but-failed one, which is what makes a repeat worth
// reporting to the model.
var failureSignal = regexp.MustCompile(`(?i)\b(exit\s*(code|status)?\s*[:=]?\s*[1-9]\d*|error|failed|failure|exception|traceback|timed?\s*out|timeout|permission denied|not found|refused)\b`)

// digitRun collapses the numbers inside a failure message so that two runs of
// the same failure compare equal even when line numbers, durations or process
// ids differ.
var digitRun = regexp.MustCompile(`\d+`)

// ToolEvidence is one tool call together with the result the client returned
// for it, if any.
type ToolEvidence struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
	Result    string `json:"result,omitempty"`
	Failed    bool   `json:"failed,omitempty"`
}

// Ledger is the evidence a client-driven tool loop has accumulated so far. It
// is rebuilt from the incoming message history on every request, because the
// server holds no state between the turns of such a loop.
type Ledger struct {
	Tasks            TaskState
	lastSignature    string
	lastResult       [sha256.Size]byte
	unchangedResults int
	// Completed holds the calls whose results are already in the history.
	Completed []ToolEvidence
	// Pending holds the calls the client announced but has not answered.
	Pending []ToolEvidence
	// Rounds counts the assistant turns that announced at least one call.
	Rounds int
	// RepeatedCall reports that the same call was issued and answered more
	// than once.
	RepeatedCall bool
	// RepeatedFailure reports that the same call failed the same way more than
	// once, which means another attempt is unlikely to behave differently.
	RepeatedFailure bool
	// RepetitionSignature names the call behind RepeatedCall or
	// RepeatedFailure, for logging.
	RepetitionSignature string
}

// LedgerCall is one announced tool call, in the provider-independent shape the
// ledger consumes.
type LedgerCall struct {
	ID        string
	Name      string
	Arguments string
}

// LedgerResult is one tool result, in the provider-independent shape the ledger
// consumes.
type LedgerResult struct {
	ID      string
	Content string
	IsError bool
}

// CanonicalArguments normalizes a tool argument string so that two calls that
// differ only in key order or whitespace compare equal. Text that is not valid
// JSON is compared after trimming, because a non-JSON argument string is still
// a stable identity for the call.
func CanonicalArguments(arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return ""
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return trimmed
	}
	// Re-marshaling an any sorts object keys, which is exactly the
	// normalization wanted here.
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return trimmed
	}
	return string(encoded)
}

// compactResult shortens a long tool result to a head and a tail around a
// marker naming how much was removed. The middle of a long log is the least
// informative part, and the reader is a model that only needs to recognize
// which result this is.
func compactResult(result string) string {
	trimmed := strings.TrimSpace(result)
	if len(trimmed) <= maxEvidenceResult {
		return trimmed
	}
	// Both cuts land on a rune boundary. The caps are byte counts, and a tool
	// result carries whatever text the tool printed, so a cut in the middle of
	// a multi-byte character would put an invalid byte in the prompt.
	head := textcut.StartAtOrBefore(trimmed, maxEvidenceResult/3)
	tail := max(maxEvidenceResult-head-minEvidenceTail, minEvidenceTail)
	tailStart := textcut.StartAtOrAfter(trimmed, len(trimmed)-tail)
	removed := tailStart - head
	return trimmed[:head] +
		"\n... [truncated " + strconv.Itoa(removed) + " bytes] ...\n" +
		trimmed[tailStart:]
}

// normalizeFailure reduces a failure message to a signature that ignores the
// numbers inside it and its tail.
func normalizeFailure(result string) string {
	normalized := digitRun.ReplaceAllString(strings.ToLower(strings.TrimSpace(result)), "#")
	if len(normalized) > maxFailureSignature {
		normalized = textcut.Truncate(normalized, maxFailureSignature)
	}
	return normalized
}

// CallSignature identifies a call by what it does, not by the id the client
// assigned it. Two turns of the same loop use different ids for the same work.
//
// Every duplicate check in this project compares signatures, so that one answer
// to "is this the same call" serves the client-driven ledger and the
// request-local loop alike.
func CallSignature(name, arguments string) string {
	return name + "\x00" + CanonicalArguments(arguments)
}

// BuildLedger reconstructs the evidence of a client-driven tool loop from the
// calls and results found in the incoming history. Calls and results are
// matched by id; a result whose call is no longer in the history (a client that
// trimmed its context) is ignored, because there is nothing to attribute it to.
//
// The caller collects calls and results from its own message shape, which keeps
// this package independent of the provider payload types.
func BuildLedger(calls []LedgerCall, results []LedgerResult, rounds int) Ledger {
	resultByID := make(map[string]LedgerResult, len(results))
	for _, result := range results {
		if result.ID != "" {
			resultByID[result.ID] = result
		}
	}

	ledger := Ledger{Rounds: rounds}
	seenCall := make(map[string]bool, len(calls))
	seenFailure := make(map[string]bool, len(calls))

	for _, call := range calls {
		evidence := ToolEvidence{ID: call.ID, Name: call.Name, Arguments: call.Arguments}
		record, answered := resultByID[call.ID]
		if !answered {
			ledger.Pending = append(ledger.Pending, evidence)
			ledger.unchangedResults = 0
			ledger.lastSignature = ""
			continue
		}
		result := record.Content
		// The failure verdict and the repetition signature read the untrimmed
		// result: the failing line can sit in the middle of a long log, which
		// is exactly the part compactResult drops.
		evidence.Failed = record.IsError || failureSignal.MatchString(result)
		evidence.Result = compactResult(result)
		ledger.Completed = append(ledger.Completed, evidence)

		signature := CallSignature(call.Name, call.Arguments)
		ledger.recordProgress(signature, result)
		if seenCall[signature] {
			ledger.RepeatedCall = true
			if ledger.RepetitionSignature == "" {
				ledger.RepetitionSignature = call.Name
			}
		}
		seenCall[signature] = true

		if !evidence.Failed {
			continue
		}
		failureKey := signature + "\x00" + normalizeFailure(result)
		if seenFailure[failureKey] {
			ledger.RepeatedFailure = true
			ledger.RepetitionSignature = call.Name
		}
		seenFailure[failureKey] = true
	}

	ledger.Tasks = TasksFromHistory(calls, results)
	return ledger
}

func (l *Ledger) recordProgress(signature, result string) {
	digest := sha256.Sum256([]byte(strings.TrimSpace(result)))
	if signature == l.lastSignature && digest == l.lastResult {
		l.unchangedResults++
	} else {
		l.unchangedResults = 1
	}
	l.lastSignature, l.lastResult = signature, digest
}

// CompletedCount reports how many times a call with this name and these
// arguments already has a result in the history.
func (l Ledger) CompletedCount(name, arguments string) int {
	signature := CallSignature(name, arguments)
	count := 0
	for _, evidence := range l.Completed {
		if CallSignature(evidence.Name, evidence.Arguments) == signature {
			count++
		}
	}
	return count
}

// FilterRepeated rejects sustained no-progress runs, not ordinary revisits.
// Any intervening operation, changed result, pending operation, or new user
// turn resets the evidence. Polling is bounded by the overall round/time limits.
func (l Ledger) FilterRepeated(calls []ToolCall) (kept, dropped []ToolCall) {
	if len(l.Completed) == 0 {
		return calls, nil
	}
	for _, call := range calls {
		if !isPollingTool(call.Name) && l.unchangedResults >= MaxUnchangedToolResults &&
			CallSignature(call.Name, string(call.Arguments)) == l.lastSignature {
			dropped = append(dropped, call)
			continue
		}
		kept = append(kept, call)
	}
	return kept, dropped
}

func isPollingTool(name string) bool {
	switch shortToolName(name) {
	case "write_stdin", "wait", "wait_agent", "wait_for_agent", "wait_for_completion":
		return true
	}
	return false
}

// RepeatedCallsNotice replaces the answer text when every tool call of a turn
// was dropped as a settled repeat. The parser clears the content whenever tool
// calls are present, so without a substitute the client would receive a turn
// with neither calls nor an answer.
const RepeatedCallsNotice = "The tools requested in this turn have already run with the same arguments, and their results are in the conversation above. Answer from those results instead of calling them again."

// EvidenceNote renders the completed calls as a compact instruction for the
// prompt, so the model treats a result it already has as settled instead of
// asking for it again. It returns an empty string when there is no evidence.
func (l Ledger) EvidenceNote() string {
	if len(l.Completed) == 0 {
		return l.Tasks.Note()
	}

	encoded, err := json.Marshal(l.Completed)
	if err != nil {
		return ""
	}

	var note strings.Builder
	note.WriteString("TOOL EVIDENCE FROM THIS CONVERSATION\n")
	note.WriteString("These tool calls ran at the time recorded; their results are historical observations, not proof that all requested work is complete:\n")
	note.Write(encoded)
	note.WriteString("\nUse these results as evidence. Re-read files and re-run checks when edits, new instructions, changed state, or verification require it, even with identical arguments. Do not repeat unchanged operations indefinitely without making progress.")
	if l.RepeatedFailure {
		note.WriteString("\nThe call named ")
		note.WriteString(l.RepetitionSignature)
		note.WriteString(" already failed the same way more than once. Change the approach or report the failure to the user instead of repeating it.")
	}
	if l.Tasks.Unfinished() {
		note.WriteString("\n\n" + l.Tasks.Note())
	}
	return note.String()
}
