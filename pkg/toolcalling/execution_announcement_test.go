package toolcalling

import "testing"

func TestExecutionAnnouncementAfterReconnect(t *testing.T) {
	for _, text := range []string{
		"The round 1 contract is confirmed. I’ll implement only `column_counts`, preserving the other queued reporting functions and all existing Kanban behavior.",
		"I'll run the checks now.", "Let me patch the validation.",
		"The remaining failure is a source-position mutation. I’ll remove it and rerun the checks.",
		"The storage schema is now in place. I’ll write the implementation next.",
	} {
		if !IsExecutionAnnouncement(text) {
			t.Fatalf("missed execution-only announcement: %s", text)
		}
	}
}

func TestExecutionAnnouncementPreservesExplanationsAndConditionalOffers(t *testing.T) {
	for _, text := range []string{
		"All checks passed; the implementation is complete.",
		"I’ll implement this once you confirm the requirement.",
		"The example says \"I'll run the checks now.\"", "I will explain how the API works.",
		"Plan:\n1. Implement the service.\n2. Test it.", "Blocked: access to the required file was denied.",
	} {
		if IsExecutionAnnouncement(text) {
			t.Fatalf("ordinary answer misclassified: %s", text)
		}
	}
}
