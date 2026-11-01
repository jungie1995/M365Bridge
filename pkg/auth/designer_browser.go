package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
)

// IsDesignerBrowserRequest limits browser observation to the real Microsoft
// Designer broker exchange. Never collect arbitrary browser tokens or traffic.
func IsDesignerBrowserRequest(rawURL, method, postData, tenant string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || method != "POST" || len(postData) > 128<<10 {
		return false
	}
	if !designerTokenEndpoint(u, tenant) {
		return false
	}
	form, err := url.ParseQuery(postData)
	if err != nil {
		return false
	}
	return designerBrowserClients(u.Query(), form) && designerBrowserScopes(form["scope"])
}

func designerTokenEndpoint(u *url.URL, tenant string) bool {
	if u.Scheme != "https" || u.Host != "login.microsoftonline.com" || u.User != nil || u.Fragment != "" {
		return false
	}
	for _, account := range []string{tenant, "organizations", "common"} {
		if account != "" && strings.EqualFold(u.Path, "/"+account+"/oauth2/v2.0/token") {
			return true
		}
	}
	return false
}

func designerBrowserClients(query, form url.Values) bool {
	for key, expected := range map[string]string{"client_id": brokerClientID, "brk_client_id": designerClientID} {
		found := false
		for _, values := range []url.Values{query, form} {
			for _, value := range values[key] {
				if value != expected {
					return false
				}
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func designerBrowserScopes(scopes []string) bool {
	if len(scopes) != 1 {
		return false
	}
	found := false
	for scope := range strings.FieldsSeq(scopes[0]) {
		switch scope {
		case "https://designerappservice.officeapps.live.com/.default":
			found = true
		case "openid", "profile", "offline_access":
		default:
			return false
		}
	}
	return found
}

// SaveDesignerBrowserCredentials accepts only a response observed directly from
// the filtered HTTPS exchange in our dedicated sign-in browser. client_info is
// checked against the already verified text identity; it is not a token import API.
func (tm *TokenManager) SaveDesignerBrowserCredentials(ctx context.Context, response []byte) error {
	result, err := parseDesignerBrowserCredentials(response)
	if err != nil {
		return err
	}
	if !tm.matchesDesignerIdentity(result.ClientInfo) {
		return errors.New("the Designer account differs; reconnect using the configured Microsoft account")
	}
	if ctx.Err() != nil {
		return errors.New("the Designer sign-in was cancelled before saving")
	}
	tm.designerMu.Lock()
	defer tm.designerMu.Unlock()
	if err := tm.writeBrokerRefreshToken(result.Refresh); err != nil {
		return errors.New("could not save the private Designer renewal credential")
	}
	cache, _ := json.Marshal(designerTokenCache{AccessToken: result.Access, ExpiresAt: time.Now().Add(time.Duration(result.Expires) * time.Second).Unix()})
	if err := atomicWriteFile(designerTokenCacheFile, cache, 0600); err != nil {
		return errors.New("could not save the private Designer access cache")
	}
	return nil
}

type designerBrowserCredentials struct {
	Access     string `json:"access_token"`
	Refresh    string `json:"refresh_token"`
	ClientInfo string `json:"client_info"`
	Expires    int    `json:"expires_in"`
}

func parseDesignerBrowserCredentials(response []byte) (designerBrowserCredentials, error) {
	var result designerBrowserCredentials
	if len(response) > 256<<10 {
		return result, errors.New("the Designer authorization response exceeded the safe size limit")
	}
	if json.Unmarshal(response, &result) != nil || len(result.Access) < 100 || len(result.Refresh) < 50 || result.Expires < 60 || result.Expires > 86400 {
		return result, errors.New("the Microsoft response did not contain renewable Designer image credentials")
	}
	return result, nil
}

func (tm *TokenManager) matchesDesignerIdentity(clientInfo string) bool {
	info, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(clientInfo, "="))
	var identity struct {
		UID    string `json:"uid"`
		Tenant string `json:"utid"`
	}
	return err == nil && json.Unmarshal(info, &identity) == nil && tm.tenant != "" && tm.userOID != "" && strings.EqualFold(identity.UID, tm.userOID) && strings.EqualFold(identity.Tenant, tm.tenant)
}

// DesignerCredentialState exposes no credential bytes and does not call Microsoft.
func (tm *TokenManager) DesignerCredentialState() string {
	if token, ok := readDesignerTokenCache(); ok && token != "" {
		return "cached"
	}
	if token, err := tm.readBrokerRefreshToken(); err == nil && token != "" {
		return "renewal_needed"
	}
	return "browser_sign_in_required"
}

// RenewSavedDesigner checks a reusable credential with Microsoft rather than
// declaring success just because an access-token cache file exists. A valid
// reconnect need not create another setup image or wait for a browser cache miss.
func (tm *TokenManager) RenewSavedDesigner(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	tm.designerMu.Lock()
	defer tm.designerMu.Unlock()
	refresh, err := tm.readBrokerRefreshToken()
	if err != nil || refresh == "" {
		return false
	}
	request := tm.requestDesignerToken
	if tm.designerTokenRequest != nil {
		request = tm.designerTokenRequest
	}
	token, expires, err := request(refresh)
	if err != nil || token == "" || expires < 60 || expires > 86400 || ctx.Err() != nil {
		return false
	}
	cache, _ := json.Marshal(designerTokenCache{AccessToken: token, ExpiresAt: time.Now().Add(time.Duration(expires) * time.Second).Unix()})
	return atomicWriteFile(designerTokenCacheFile, cache, 0600) == nil
}
