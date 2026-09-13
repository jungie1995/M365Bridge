package servers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/client"
)

func newResponsesEventStream(api *APIServer, w http.ResponseWriter, flusher http.Flusher, id, model string, created int64) *responsesStream {
	return &responsesStream{api: api, w: w, flusher: flusher, responseID: id, createdAt: created, model: model, msgID: "msg_" + id, reasoningID: "rs_" + id}
}

func (s *responsesStream) begin() {
	for _, event := range []string{"response.created", "response.in_progress"} {
		s.event(event, map[string]any{"response": responsesStatusObject(s.responseID, s.model, "in_progress", s.createdAt)})
	}
}

func (s *responsesStream) end(response map[string]any) {
	event := "response.completed"
	if response["status"] == "incomplete" {
		event = "response.incomplete"
	}
	s.event(event, map[string]any{"response": response})
	if !s.failedSent {
		_, _ = fmt.Fprint(s.w, "data: [DONE]\n\n")
		s.flusher.Flush()
	}
}

func (s *responsesStream) collectCompactionText(ctx context.Context, ch <-chan client.StreamChunk) (string, error) {
	var text strings.Builder
	keepalive := time.NewTicker(sseKeepaliveInterval)
	defer keepalive.Stop()
	sawFinal := false
	for {
		chunk, more := nextStreamChunk(ctx, ch, keepalive, s.w, s.flusher, func() error { return writeSSEKeepalive(s.w, s.flusher) })
		if !more {
			break
		}
		if chunk.Error != nil {
			return "", chunk.Error
		}
		if chunk.FinishReason == "length" {
			return "", errCompactionIncomplete
		}
		text.WriteString(chunk.Text)
		if chunk.IsFinal {
			sawFinal = true
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !sawFinal {
		return "", client.ErrConnectionClosed
	}
	return text.String(), nil
}

func compactionTruncated(text, finishReason string, maxTokens int) bool {
	return finishReason == "length" || (maxTokens > 0 && countTokens(text) > maxTokens)
}
