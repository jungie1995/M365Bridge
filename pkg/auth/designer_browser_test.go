package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestDesignerReconnectVerifiesSavedRenewalAndFallsBackOnExpiry(t *testing.T) {
	useTemporaryWorkingDirectory(t)
	tm := NewTokenManager("tenant", "client", "scope", "refresh", "cache")
	if tm.RenewSavedDesigner(context.Background()) {
		t.Fatal("missing credential appeared renewed")
	}
	if err := tm.writeBrokerRefreshToken("fixture-renewal"); err != nil {
		t.Fatal(err)
	}
	tm.designerTokenRequest = func(token string) (string, int, error) {
		if token != "fixture-renewal" {
			t.Fatal("wrong renewal credential")
		}
		return "new-access", 3600, nil
	}
	if !tm.RenewSavedDesigner(context.Background()) {
		t.Fatal("valid renewal failed")
	}
	tm.designerTokenRequest = func(string) (string, int, error) { return "", 0, errors.New("expired or revoked") }
	if tm.RenewSavedDesigner(context.Background()) {
		t.Fatal("cache hit concealed failed renewal")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if tm.RenewSavedDesigner(ctx) {
		t.Fatal("cancelled renewal succeeded")
	}
}

func TestDesignerBrowserRequestScope(t *testing.T) {
	u := "https://login.microsoftonline.com/tenant/oauth2/v2.0/token?brk_client_id=" + designerClientID
	body := url.Values{"client_id": {brokerClientID}, "scope": {designerBrokerScope}}.Encode()
	if !IsDesignerBrowserRequest(u, "POST", body, "tenant") {
		t.Fatal("expected Designer exchange rejected")
	}
	for _, bad := range []struct{ url, method, body string }{
		{strings.Replace(u, "login.microsoftonline.com", "attacker.example", 1), "POST", body},
		{strings.Replace(u, "https://", "http://", 1), "POST", body},
		{strings.Replace(u, "/tenant/", "/other/", 1), "POST", body},
		{u, "GET", body}, {u, "POST", body + "&scope=openid"},
		{u, "POST", body + "&client_id=another-client"},
		{u + "&client_id=another-client", "POST", body},
		{u, "POST", strings.Replace(body, "designerappservice.officeapps.live.com", "graph.microsoft.com", 1)},
		{u, "POST", body + "+https%3A%2F%2Fgraph.microsoft.com%2F.default"},
	} {
		if IsDesignerBrowserRequest(bad.url, bad.method, bad.body, "tenant") {
			t.Fatal("unrelated or ambiguous token request accepted")
		}
	}
}

func designerBrowserFixture(tenant, oid string) []byte {
	info, _ := json.Marshal(map[string]string{"uid": oid, "utid": tenant})
	data, _ := json.Marshal(map[string]any{"access_token": strings.Repeat("a", 256), "refresh_token": strings.Repeat("r", 150), "expires_in": 3600, "client_info": base64.RawURLEncoding.EncodeToString(info)})
	return data
}

func TestDesignerBrowserFreshAccountSavesPrivateCredentials(t *testing.T) {
	useTemporaryWorkingDirectory(t)
	tm := NewTokenManager("tenant", "client", "scope", "refresh", "cache")
	tm.SetUserOID("oid")
	if tm.DesignerCredentialState() != "browser_sign_in_required" {
		t.Fatal("new installation falsely ready")
	}
	if err := tm.SaveDesignerBrowserCredentials(context.Background(), designerBrowserFixture("tenant", "oid")); err != nil {
		t.Fatal(err)
	}
	if tm.DesignerCredentialState() != "cached" {
		t.Fatal("Designer cache not saved")
	}
	raw, err := os.ReadFile(designerBrokerRefreshFile)
	if err != nil || strings.Contains(string(raw), strings.Repeat("r", 150)) {
		t.Fatal("renewal credential not encrypted")
	}
	if token, err := tm.readBrokerRefreshToken(); err != nil || token != strings.Repeat("r", 150) {
		t.Fatal("renewal credential not reusable")
	}
}

func TestDesignerBrowserRejectsWrongAccountAndCancellationWithoutOverwrite(t *testing.T) {
	useTemporaryWorkingDirectory(t)
	tm := NewTokenManager("tenant", "client", "scope", "refresh", "cache")
	tm.SetUserOID("oid")
	if err := tm.SaveDesignerBrowserCredentials(context.Background(), designerBrowserFixture("tenant", "oid")); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(designerBrokerRefreshFile)
	for _, bad := range [][]byte{designerBrowserFixture("other", "oid"), designerBrowserFixture("tenant", "other"), []byte(`{"error":"credential-must-not-leak"}`), []byte("not-json"), []byte(strings.Repeat("x", 257<<10))} {
		err := tm.SaveDesignerBrowserCredentials(context.Background(), bad)
		if err == nil || strings.Contains(err.Error(), "credential-must-not-leak") {
			t.Fatal("invalid credentials not rejected safely")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := tm.SaveDesignerBrowserCredentials(ctx, designerBrowserFixture("tenant", "oid")); err == nil {
		t.Fatal("cancelled login saved")
	}
	after, _ := os.ReadFile(designerBrokerRefreshFile)
	if string(before) != string(after) {
		t.Fatal("working credentials overwritten")
	}
}

func TestBrokerCookiesNeverLeaveMicrosoftLogin(t *testing.T) {
	for _, target := range []string{"https://evil.example/", "https://login.microsoftonline.com.attacker.example/", "https://user@login.microsoftonline.com/", "http://login.microsoftonline.com/", "https://m365.cloud.microsoft/"} {
		if _, err := brokerBrowserRequest(target, "", "private-cookie"); err == nil {
			t.Fatal("cookie sent to unintended host")
		}
	}
	request, err := brokerBrowserRequest("https://login.microsoftonline.com/tenant/oauth2/v2.0/authorize", "", "private-cookie")
	if err != nil || request.Header.Get("Cookie") != "private-cookie" {
		t.Fatal("own login cookies not attached")
	}
}
