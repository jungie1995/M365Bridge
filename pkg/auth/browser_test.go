package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func browserTestToken(oid string) string {
	data, _ := json.Marshal(map[string]string{"tid": "tenant", "oid": oid, "aud": "https://substrate.office.com/sydney"})
	return "header." + base64.RawURLEncoding.EncodeToString(data) + ".signature"
}

func browserTestCallback(challenge *BrowserChallenge) string {
	u, _ := url.Parse(challenge.AuthorizationURL)
	return defaultRedirectURI + "?" + url.Values{"state": {u.Query().Get("state")}, "code": {"test-code"}}.Encode()
}

func TestBrowserChallengeUsesStateAndPKCE(t *testing.T) {
	tm := NewTokenManager("tenant", "client", "scope", "refresh", "cache")
	challenge, err := tm.BeginBrowserLogin()
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(challenge.AuthorizationURL)
	digest := sha256.Sum256([]byte(challenge.verifier))
	if u.Query().Get("code_challenge") != base64.RawURLEncoding.EncodeToString(digest[:]) || u.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("PKCE challenge mismatch")
	}
	if !challenge.MatchesRedirect(browserTestCallback(challenge)) {
		t.Fatal("own callback was rejected")
	}
	for _, raw := range []string{
		strings.Replace(browserTestCallback(challenge), "m365.cloud.microsoft", "example.com", 1),
		defaultRedirectURI + "?state=wrong&code=test-code",
		strings.Replace(browserTestCallback(challenge), "https://", "http://", 1),
		strings.Replace(browserTestCallback(challenge), "https://", "https://user@", 1),
	} {
		if challenge.MatchesRedirect(raw) {
			t.Fatal("foreign callback accepted")
		}
	}
}

func TestBrowserLoginVerifiesAccountBeforeReplacingCredentials(t *testing.T) {
	useTemporaryWorkingDirectory(t)
	_ = os.WriteFile("refresh", []byte("existing-refresh"), 0600)
	_ = os.WriteFile("cache", []byte("existing-cache"), 0600)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": browserTestToken("other-account"), "refresh_token": "new-refresh", "expires_in": 3600})
	}))
	defer server.Close()
	tm := NewTokenManager("tenant", "client", "scope", "refresh", "cache")
	tm.SetUserOID("expected-account")
	tm.tokenURL = server.URL
	challenge, _ := tm.BeginBrowserLogin()
	err := tm.CompleteBrowserLogin(context.Background(), challenge, browserTestCallback(challenge), []SSOCookie{{Name: "ESTSAUTH", Value: "cookie", Domain: "login.microsoftonline.com"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "different Microsoft account") {
		t.Fatal("wrong account not rejected")
	}
	old, _ := os.ReadFile("refresh")
	cached, _ := os.ReadFile("cache")
	if string(old) != "existing-refresh" || string(cached) != "existing-cache" {
		t.Fatal("working credentials were replaced before identity verification")
	}
}

func TestBrowserLoginSavesRenewableCredentialsWithoutExport(t *testing.T) {
	useTemporaryWorkingDirectory(t)
	var verifier string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		verifier = r.Form.Get("code_verifier")
		if r.Form.Get("code") != "test-code" || r.Header.Get("Origin") != "https://m365.cloud.microsoft" {
			t.Error("invalid exchange")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": browserTestToken("expected-account"), "refresh_token": "new-refresh", "expires_in": 3600})
	}))
	defer server.Close()
	tm := NewTokenManager("tenant", "client", "scope", "refresh", "cache")
	tm.SetUserOID("expected-account")
	tm.tokenURL = server.URL
	challenge, _ := tm.BeginBrowserLogin()
	expectedVerifier := challenge.verifier
	err := tm.CompleteBrowserLogin(context.Background(), challenge, browserTestCallback(challenge), []SSOCookie{{Name: "ESTSAUTH", Value: "cookie", Domain: "login.microsoftonline.com"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if verifier != expectedVerifier {
		t.Fatal("wrong verifier redeemed")
	}
	refresh, err := tm.readRefreshToken()
	if err != nil || refresh != "new-refresh" {
		t.Fatal("renewed refresh token was not saved")
	}
	encoded, _ := os.ReadFile("refresh")
	if strings.Contains(string(encoded), "new-refresh") {
		t.Fatal("refresh token was saved unencrypted")
	}
	if challenge.state != "" || challenge.verifier != "" {
		t.Fatal("completed challenge was not invalidated")
	}
}

func TestBrowserLoginRejectsUnmatchedCallbackBeforeNetwork(t *testing.T) {
	tm := NewTokenManager("tenant", "client", "scope", "refresh", "cache")
	tm.tokenURL = "http://127.0.0.1:1"
	challenge, _ := tm.BeginBrowserLogin()
	if err := tm.CompleteBrowserLogin(context.Background(), challenge, defaultRedirectURI+"?state=foreign&code=secret", nil, nil); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("invalid callback was not safely rejected")
	}
}
