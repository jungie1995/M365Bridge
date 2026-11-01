package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestBrowserFirstLoginDiscoversOnlyRedeemedIdentity(t *testing.T) {
	for _, scenario := range []string{"success", "bad-audience", "missing-oid", "missing-cookies", "save-failure"} {
		t.Run(scenario, func(t *testing.T) {
			useTemporaryWorkingDirectory(t)
			tm, claims := browserSetupTestManager(t, scenario)
			challenge, _ := tm.BeginBrowserLogin()
			u, _ := url.Parse(challenge.AuthorizationURL)
			if !strings.Contains(u.Path, "/organizations/") || u.Query().Get("prompt") != "select_account" {
				t.Fatal("first login did not offer account selection")
			}
			cookies := []SSOCookie{{Name: "ESTSAUTH", Value: "cookie", Domain: "login.microsoftonline.com"}}
			if scenario == "missing-cookies" {
				cookies = nil
			}
			called := false
			persist := func(tenant, oid string) error {
				called = true
				if tenant != claims["tid"] || oid != claims["oid"] {
					t.Fatal("wrong identity persisted")
				}
				if scenario == "save-failure" {
					return errors.New("private disk failure")
				}
				return nil
			}
			err := tm.CompleteBrowserSetup(context.Background(), challenge, browserTestCallback(challenge), cookies, nil, persist)
			checkBrowserSetupOutcome(t, scenario, err, called, challenge)
		})
	}
}

func browserSetupTestManager(t *testing.T, scenario string) (*TokenManager, map[string]string) {
	t.Helper()
	claims := map[string]string{"tid": "11111111-1111-4111-8111-111111111111", "oid": "22222222-2222-4222-8222-222222222222", "aud": "https://substrate.office.com/sydney"}
	if scenario == "bad-audience" {
		claims["aud"] = "another-service"
	}
	if scenario == "missing-oid" {
		delete(claims, "oid")
	}
	data, _ := json.Marshal(claims)
	token := "header." + base64.RawURLEncoding.EncodeToString(data) + ".signature"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": token, "refresh_token": "new-private-refresh", "expires_in": 3600})
	}))
	t.Cleanup(server.Close)
	tm := NewTokenManager("organizations", "client", "scope", "data/tokens/refresh", "data/tokens/cache")
	tm.tokenURL = server.URL
	return tm, claims
}

func checkBrowserSetupOutcome(t *testing.T, scenario string, err error, called bool, challenge *BrowserChallenge) {
	t.Helper()
	switch scenario {
	case "success":
		if err != nil || !called {
			t.Fatalf("first sign-in failed: %v", err)
		}
		if challenge.state != "" {
			t.Fatal("challenge remained reusable")
		}
	case "save-failure":
		if err == nil || strings.Contains(err.Error(), "private disk failure") {
			t.Fatal("unsafe or missing persistence failure")
		}
	default:
		checkBrowserSetupRejected(t, err, called)
	}
}

func checkBrowserSetupRejected(t *testing.T, err error, called bool) {
	t.Helper()
	if err == nil || called {
		t.Fatal("invalid identity or cookie evidence was accepted")
	}
	if _, err := os.Stat("data/tokens/refresh"); !os.IsNotExist(err) {
		t.Fatal("credentials written before validation")
	}
}

func TestExistingBrowserAccountCannotSwitchThroughSetup(t *testing.T) {
	tm := NewTokenManager("configured-tenant", "client", "scope", "refresh", "cache")
	tm.SetUserOID("configured-user")
	challenge, _ := tm.BeginBrowserLogin()
	if tm.CompleteBrowserSetup(context.Background(), challenge, browserTestCallback(challenge), nil, nil, func(string, string) error { return nil }) == nil {
		t.Fatal("existing identity was allowed to switch")
	}
}
