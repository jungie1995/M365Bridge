package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// BrowserChallenge keeps the PKCE verifier and state in memory for one login.
// Neither is persisted or exposed in status messages.
type BrowserChallenge struct {
	AuthorizationURL string
	verifier         string
	state            string
}

func (tm *TokenManager) BeginBrowserLogin() (*BrowserChallenge, error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return nil, errors.New("could not start browser authentication")
	}
	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return nil, errors.New("could not generate sign-in state")
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)
	params := url.Values{
		"client_id": {tm.clientID}, "response_type": {"code"},
		"redirect_uri": {defaultRedirectURI}, "scope": {tm.scope},
		"response_mode": {"query"}, "code_challenge": {challenge},
		"code_challenge_method": {"S256"}, "state": {state},
	}
	return &BrowserChallenge{AuthorizationURL: fmt.Sprintf(authorizeURLTemplate, tm.tenant) + "?" + params.Encode(), verifier: verifier, state: state}, nil
}

func (challenge *BrowserChallenge) MatchesRedirect(raw string) bool {
	if challenge == nil || challenge.state == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "m365.cloud.microsoft" || u.User != nil || u.Path != "/spalanding" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(browserCallbackParams(u).Get("state")), []byte(challenge.state)) == 1
}

func browserCallbackParams(u *url.URL) url.Values {
	values := u.Query()
	if values.Get("state") == "" && u.Fragment != "" {
		values, _ = url.ParseQuery(u.Fragment)
	}
	return values
}

func browserAccountMatches(token, tenant, oid string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Tenant   string `json:"tid"`
		OID      string `json:"oid"`
		Audience string `json:"aud"`
	}
	if json.Unmarshal(data, &claims) != nil {
		return false
	}
	return strings.EqualFold(claims.Tenant, tenant) && strings.EqualFold(claims.OID, oid) && claims.Audience == "https://substrate.office.com/sydney"
}

// CompleteBrowserLogin validates the callback and identity before touching the
// existing bridge credentials. Tokens come directly from Microsoft's HTTPS endpoint.
func (tm *TokenManager) CompleteBrowserLogin(ctx context.Context, challenge *BrowserChallenge, callback string, loginCookies, webCookies []SSOCookie) error {
	tm.refreshMu.Lock()
	defer tm.refreshMu.Unlock()
	code, err := browserAuthorizationCode(challenge, callback)
	if err != nil {
		return err
	}
	tokens, err := tm.requestAuthCode(ctx, code, challenge.verifier)
	if err != nil {
		return browserExchangeError(err)
	}
	if err := tm.validateBrowserCredentials(tokens, loginCookies); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return errors.New("sign-in was cancelled before credentials were saved")
	}
	if err := tm.saveBrowserCredentials(tokens, loginCookies, webCookies); err != nil {
		return err
	}
	challenge.state, challenge.verifier = "", ""
	return nil
}

func browserAuthorizationCode(challenge *BrowserChallenge, callback string) (string, error) {
	if challenge == nil || !challenge.MatchesRedirect(callback) {
		return "", errors.New("sign-in callback did not match the active login")
	}
	u, _ := url.Parse(callback)
	parameters := browserCallbackParams(u)
	if parameters.Get("error") != "" {
		return "", errors.New("sign-in was declined or cancelled by Microsoft")
	}
	code := parameters.Get("code")
	if code == "" {
		return "", errors.New("no authorization code was returned by Microsoft")
	}
	return code, nil
}

func browserExchangeError(err error) error {
	if code := aadstsCode(err.Error()); code != "" {
		return fmt.Errorf("token exchange with Microsoft did not complete (%s)", code)
	}
	return errors.New("token exchange with Microsoft did not complete; try the sign-in window again")
}

func (tm *TokenManager) validateBrowserCredentials(tokens authCodeTokens, loginCookies []SSOCookie) error {
	if tokens.RefreshToken == "" || tokens.AccessToken == "" || tokens.ExpiresIn <= 0 {
		return errors.New("no renewable credentials were returned by Microsoft")
	}
	if !browserAccountMatches(tokens.AccessToken, tm.tenant, tm.userOID) {
		return errors.New("a different Microsoft account was selected; sign in with the account already configured for this bridge")
	}
	if len(loginCookies) == 0 {
		return errors.New("the sign-in session cookies were unavailable; complete sign-in in the dedicated browser window")
	}
	return nil
}

func (tm *TokenManager) saveBrowserCredentials(tokens authCodeTokens, loginCookies, webCookies []SSOCookie) error {
	if err := tm.writeRefreshToken(tokens.RefreshToken); err != nil {
		return errors.New("could not save the renewed bridge credential")
	}
	if err := tm.writeCache(TokenCache{AccessToken: tokens.AccessToken, ExpiresAt: time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second).Unix()}); err != nil {
		return errors.New("could not save the renewed access-token cache")
	}
	if err := SaveSSOCookies(loginCookies); err != nil {
		return errors.New("signed in, but could not save the session for automatic renewal")
	}
	if len(webCookies) > 0 {
		if err := SaveM365Cookies(webCookies); err != nil {
			return errors.New("signed in, but could not save the M365 web session")
		}
	}
	return nil
}
