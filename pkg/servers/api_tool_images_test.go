package servers

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSimulatedResponsesUploadsToolImagesWithoutEmbeddingTheirBytes(t *testing.T) {
	input := []any{map[string]any{"type": "function_call_output", "call_id": "image-call", "output": []any{
		map[string]any{"type": "input_text", "text": "Review this image"},
		map[string]any{"type": "input_image", "image_url": testPNGDataURL},
	}}}
	request, _ := json.Marshal(map[string]any{"input": input, "model": "auto", "tools": []any{}})
	messages := responsesInputToMessages(input)
	injectSimulatedPromptResponses(&messages, string(request), "auto", "")
	if len(messages) != 1 || len(messages[0].Images) != 1 {
		t.Fatal("canonical prompt lost image attachment")
	}
	if strings.Contains(messages[0].Content, "base64") || strings.Contains(messages[0].Content, "iVBOR") {
		t.Fatal("binary image was embedded in text prompt")
	}
	if !strings.Contains(messages[0].Content, "Review this image") || !strings.Contains(messages[0].Content, "image-call") {
		t.Fatal("tool text or call association lost")
	}
}

func TestResponsesToolImageOutputDoesNotBecomeBase64Text(t *testing.T) {
	for _, kind := range []string{"function_call_output", "custom_tool_call_output"} {
		t.Run(kind, func(t *testing.T) {
			messages := responsesInputToMessages([]any{map[string]any{
				"type": kind, "call_id": "view-scene", "output": []any{
					map[string]any{"type": "input_text", "text": "Inspect the scene"},
					map[string]any{"type": "input_image", "image_url": testPNGDataURL},
				},
			}})
			if len(messages) != 1 || len(messages[0].Images) != 1 {
				t.Fatal("tool image was not preserved as an attachment")
			}
			msg := messages[0]
			if !strings.Contains(msg.Content, "Inspect the scene") || msg.ToolCallID != "view-scene" {
				t.Fatal("lost text or call identity")
			}
			if strings.Contains(msg.Content, "base64") || strings.Contains(msg.ToolResults[0].Content, "base64") {
				t.Fatal("image bytes leaked into textual history")
			}
		})
	}
}

func TestResponsesToolPlainOutputRemainsReadable(t *testing.T) {
	for _, output := range []any{"saved scene.png", map[string]any{"saved": true}} {
		msg := responsesToolOutputMessage(map[string]any{"call_id": "save", "output": output})
		if !strings.Contains(msg.Content, "saved") || len(msg.Images) != 0 {
			t.Fatal("plain tool output changed")
		}
	}
}
