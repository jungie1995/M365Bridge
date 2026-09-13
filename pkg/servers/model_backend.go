package servers

import (
	"context"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/client"
	"github.com/KilimcininKorOglu/M365Bridge/pkg/payload"
)

// modelBackend separates the external Microsoft transport from API protocol
// handling. Tests can exercise real HTTP/SSE handlers without a live account.
// Tool execution remains with the caller unless built-in tools are opted in.
type modelBackend interface {
	ChatConversation([]payload.Message, string, string, string, string, string, bool) (string, string, []client.ToolCall, string, string, error)
	ChatConversationContext(context.Context, []payload.Message, string, string, string, string, string, bool) (string, string, []client.ToolCall, string, string, error)
	ChatConversationStreamGenContext(context.Context, []payload.Message, string, string, string, string, string, bool) <-chan client.StreamChunk
	UploadFile(string, string, string, string, string, string) (*client.UploadResult, error)
	GetPersonalizationFlags(string, string) (*client.PersonalizationFlags, error)
	SetMemoryEnabled(string, string, bool) (*client.PersonalizationFlags, error)
	SetThrottlingObserver(func(*client.ThrottlingInfo))
	SetWebSearchEnabled(bool)
}
