package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/models"
)

func TestMixedClientRefreshUsesOriginOnlyWhenMicrosoftRequiresIt(t *testing.T) {
	for _, scenario := range []string{"native", "spa", "spa-rejected-again", "unrelated-error", "other-client"} {
		t.Run(scenario, func(t *testing.T) {
			useTemporaryWorkingDirectory(t)
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
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
			}))
			defer srv.Close()
			tm := newTestTokenManager(t, srv.URL)
			tm.clientID = models.DefaultClientID
			if scenario == "other-client" {
				tm.clientID = models.DefaultDeviceClientID
			}
			token, err := tm.Refresh()
			wantCalls := 1
			if scenario == "spa" || scenario == "spa-rejected-again" {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("got %d exchanges, want %d", calls, wantCalls)
			}
			if scenario == "native" || scenario == "spa" {
				if err != nil || token != "refreshed-access" {
					t.Fatalf("refresh failed: %v", err)
				}
			} else if err == nil {
				t.Fatal("rejected refresh reported success")
			}
		})
	}
}
