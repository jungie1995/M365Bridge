package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/models"
)

func TestMixedClientRefreshUsesOriginOnlyWhenMicrosoftRequiresIt(t *testing.T) {
	cases := []struct {
		name        string
		clientID    string
		wantCalls   int
		wantSuccess bool
	}{
		{"native", models.DefaultClientID, 1, true},
		{"spa", models.DefaultClientID, 2, true},
		{"spa-rejected-again", models.DefaultClientID, 2, false},
		{"unrelated-error", models.DefaultClientID, 1, false},
		{"other-client", models.DefaultDeviceClientID, 1, false},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			useTemporaryWorkingDirectory(t)
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				assertMixedRefreshRequest(t, r, calls)
				writeMixedRefreshResponse(w, scenario.name, calls)
			}))
			defer srv.Close()
			tm := newTestTokenManager(t, srv.URL)
			tm.clientID = scenario.clientID
			token, err := tm.Refresh()
			if calls != scenario.wantCalls {
				t.Fatalf("got %d exchanges, want %d", calls, scenario.wantCalls)
			}
			if scenario.wantSuccess {
				if err != nil || token != "refreshed-access" {
					t.Fatalf("refresh failed: %v", err)
				}
			} else if err == nil {
				t.Fatal("rejected refresh reported success")
			}
		})
	}
}

func assertMixedRefreshRequest(t *testing.T, r *http.Request, calls int) {
	t.Helper()
	if err := r.ParseForm(); err != nil {
		t.Error(err)
	}
	if r.Form.Get("refresh_token") != "initial-token" {
		t.Error("changed token during a rejected redemption")
	}
	if calls == 1 && r.Header.Get("Origin") != "" {
		t.Error("native redemption received an unsolicited Origin")
	}
	if calls == 2 && r.Header.Get("Origin") != "https://m365.cloud.microsoft" {
		t.Error("SPA redemption missing registered web Origin")
	}
}

func writeMixedRefreshResponse(w http.ResponseWriter, scenario string, calls int) {
	w.Header().Set("Content-Type", "application/json")
	if scenario == "native" || scenario == "spa" && calls == 2 {
		fmt.Fprint(w, `{"access_token":"refreshed-access","refresh_token":"rotated","expires_in":3600}`)
		return
	}
	w.WriteHeader(http.StatusBadRequest)
	if scenario == "unrelated-error" {
		fmt.Fprint(w, interactionBody)
		return
	}
	fmt.Fprint(w, `{"error":"invalid_request","error_description":"AADSTS9002327: SPA redemption requires Origin"}`)
}
