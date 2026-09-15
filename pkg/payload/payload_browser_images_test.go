package payload

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBrowserImageProfileUsesCapturedImageRoute(t *testing.T) {
	t.Setenv("M365_BROWSER_IMAGE_ROUTING", "1")
	raw, err := BuildConversationPayload("session", "session", []Message{{Role: "user", Content: "apple"}}, false, "Magic", "", false, false, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Arguments []map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatal(err)
	}
	args := parsed.Arguments[0]
	if args["source"] != "owahub" || args["tone"] != "Magic" {
		t.Fatal("wrong image route")
	}
	if !strings.Contains(raw, "enterprise_flux_image") || !strings.Contains(raw, "GenerateGraphicArt") {
		t.Fatal("missing image capability")
	}
	if strings.Contains(raw, "access_token") {
		t.Fatal("credentials must stay out of payload")
	}
}

func TestBrowserProfileDoesNotAddImageToolsToCodingPayload(t *testing.T) {
	t.Setenv("M365_BROWSER_IMAGE_ROUTING", "1")
	raw, err := BuildConversationPayload("session", "session", []Message{{Role: "user", Content: "code"}}, false, "Magic", "", false, true, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "enterprise_flux_image") || strings.Contains(raw, "bizchat-as-gpt-scenario") {
		t.Fatal("browser image tools leaked into coding payload")
	}
}
