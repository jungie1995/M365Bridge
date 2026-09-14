package servers

import (
	"context"
	"time"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/client"
)

func readAnthropicStream(ctx context.Context, stream *anthropicStream, ch <-chan client.StreamChunk, maxTokens int, tools bool, stops *stopSequenceWriter, sid string) bool {
	keepalive := time.NewTicker(sseKeepaliveInterval)
	defer keepalive.Stop()
	for {
		chunk, more := nextStreamChunk(ctx, ch, keepalive, stream.w, stream.flusher, func() error { return writeAnthropicKeepalive(stream.w, stream.flusher) })
		if !more {
			return ctx.Err() == nil
		}
		switch stream.chunk(chunk, ch, maxTokens, tools, stops, sid) {
		case streamLoopFailed:
			return false
		case streamLoopStop:
			return true
		}
	}
}
