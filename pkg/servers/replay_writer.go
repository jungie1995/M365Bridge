package servers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

var errReplayCapacity = errors.New("idempotency response exceeds replay capacity")

type replayWriter struct {
	http.ResponseWriter
	body   bytes.Buffer
	status int
	sent   bool
	err    error
	cancel context.CancelFunc
}

type flushingReplayWriter struct {
	*replayWriter
	flusher http.Flusher
}

func (w *replayWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *replayWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *replayWriter) sendHeaders() {
	if w.sent {
		return
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.ResponseWriter.WriteHeader(w.status)
	w.sent = true
}
func (w *flushingReplayWriter) Flush() { w.sendHeaders(); w.flusher.Flush() }

func (w *replayWriter) Write(data []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.body.Len()+len(data) > replayBodyMax {
		w.err = errReplayCapacity
		w.cancel()
		return 0, w.err
	}
	w.sendHeaders()
	_, _ = w.body.Write(data)
	n, err := w.ResponseWriter.Write(data)
	if err != nil {
		w.err = err
		w.cancel()
	}
	return n, err
}

// On overflow the first response terminates explicitly too. The durable running
// marker prevents another execution even when its complete wire body won't fit.
func (api *APIServer) replayCapacityError(w *replayWriter, path string) {
	message := "The response exceeded the 2 MiB idempotency replay limit. Reconcile received tool results before resuming with a new key and smaller output."
	if !w.sent {
		api.sendErrorCode(w.ResponseWriter, 413, "idempotency_capacity", message)
		return
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
		return
	}
	event := map[string]any{"type": "error", "error": map[string]any{"type": "server_error", "code": "idempotency_capacity", "message": message}}
	if strings.HasPrefix(path, "/v1/responses") {
		event = map[string]any{"type": "error", "code": "idempotency_capacity", "message": message, "param": nil, "sequence_number": nextReplaySequence(w.body.Bytes())}
	}
	encoded, _ := json.Marshal(event)
	_, _ = fmt.Fprintf(w.ResponseWriter, "event: error\ndata: %s\n\n", encoded)
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func nextReplaySequence(body []byte) int {
	next := 0
	for line := range bytes.SplitSeq(body, []byte("\n")) {
		data, ok := bytes.CutPrefix(line, []byte("data: "))
		if !ok {
			continue
		}
		var event struct {
			Sequence *int `json:"sequence_number"`
		}
		if json.Unmarshal(data, &event) == nil && event.Sequence != nil {
			next = max(next, *event.Sequence+1)
		}
	}
	return next
}
