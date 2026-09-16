package servers

import (
	"encoding/json"
	"github.com/KilimcininKorOglu/M365Bridge/pkg/models"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestV1HealthReportsReachability(t *testing.T) {
	api := &APIServer{}
	recorder := httptest.NewRecorder()
	api.handleV1Health(recorder, httptest.NewRequest(http.MethodGet, "/v1/health", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q, want application/json", got)
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("health body is not JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("status field = %q, want ok", body["status"])
	}
}

func TestReadinessDistinguishesImagesFromTextWithoutInferringUpstreamAccess(t *testing.T) {
	t.Chdir(t.TempDir())
	_ = os.MkdirAll("data/tokens", 0700)
	_ = os.WriteFile("data/tokens/rt_90day.txt", []byte("encrypted-fixture"), 0600)
	api := &APIServer{config: &models.Config{TenantID: "fixture-tenant", UserOID: "fixture-user"}}
	for _, mode := range []string{"", "1"} {
		t.Setenv("M365_BROWSER_IMAGE_ROUTING", mode)
		w := httptest.NewRecorder()
		api.handleBridgeReadiness(w, httptest.NewRequest(http.MethodGet, "/v1/bridge/readiness", nil))
		var body map[string]any
		if json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatal("invalid JSON")
		}
		if body["image_routing"] != (mode == "1") || body["upstream_verified"] != false || body["credential_saved"] != true {
			t.Fatal("misleading readiness")
		}
		if _, exists := body["tenant"]; exists {
			t.Fatal("identity leaked")
		}
	}
}

func TestHealthStaysPlainText(t *testing.T) {
	// The original /health route predates any client contract on its body, so
	// it keeps returning OK as text.
	api := &APIServer{}
	recorder := httptest.NewRecorder()
	api.handleHealth(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if recorder.Body.String() != "OK" {
		t.Fatalf("body = %q, want OK", recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("content type = %q, want text/plain; charset=utf-8", got)
	}
	// A cached liveness answer would report a process that is no longer there.
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// The JSON probe inherits the refusal from the shared writer.
func TestV1HealthRefusesStorage(t *testing.T) {
	api := &APIServer{}
	recorder := httptest.NewRecorder()
	api.handleV1Health(recorder, httptest.NewRequest(http.MethodGet, "/v1/health", nil))

	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}
