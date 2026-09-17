// Package browserlogin implements an explicit browser-assisted sign-in using a
// dedicated Edge profile. It observes only its own OAuth redirect and reads only
// cookies applicable to the Microsoft login/portal URLs in that profile.
package browserlogin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/atomicfile"
	"github.com/KilimcininKorOglu/M365Bridge/pkg/auth"
	"github.com/KilimcininKorOglu/M365Bridge/pkg/models"
	"github.com/KilimcininKorOglu/M365Bridge/pkg/setup"
	"github.com/gorilla/websocket"
)

type Options struct {
	StatusFile string
	Timeout    time.Duration
	AttemptID  string
}
type status struct {
	State     string `json:"state"`
	Message   string `json:"message"`
	UpdatedAt string `json:"updated_at"`
	PID       int    `json:"pid"`
	AttemptID string `json:"attempt_id"`
}

func writeStatus(options Options, state, message string) error {
	if options.StatusFile == "" {
		return nil
	}
	data, _ := json.Marshal(status{state, message, time.Now().UTC().Format(time.RFC3339), os.Getpid(), options.AttemptID})
	if err := os.MkdirAll(filepath.Dir(options.StatusFile), 0700); err != nil {
		return err
	}
	return atomicfile.Write(options.StatusFile, data, 0600)
}

func edgeExecutable() (string, error) {
	for _, parent := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LOCALAPPDATA")} {
		if parent == "" {
			continue
		}
		candidate := filepath.Join(parent, "Microsoft", "Edge", "Application", "msedge.exe")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("could not find Microsoft Edge on this computer")
}

func debuggerAddress(contents string) (string, error) {
	lines := strings.Fields(contents)
	if len(lines) < 2 {
		return "", errors.New("browser debugger is not ready")
	}
	port, err := strconv.Atoi(lines[0])
	if err != nil || port < 1024 || port > 65535 || !strings.HasPrefix(lines[1], "/devtools/browser/") || strings.ContainsAny(lines[1], "?#@") {
		return "", errors.New("invalid dedicated-browser control endpoint")
	}
	return fmt.Sprintf("ws://127.0.0.1:%d%s", port, lines[1]), nil
}

type message struct {
	ID        int             `json:"id"`
	Method    string          `json:"method"`
	SessionID string          `json:"sessionId"`
	Params    json.RawMessage `json:"params"`
	Result    json.RawMessage `json:"result"`
	Error     json.RawMessage `json:"error"`
}

type browser struct {
	conn      *websocket.Conn
	nextID    int
	session   string
	challenge *auth.BrowserChallenge
	callback  string
	designer  *designerObserver
}

func (b *browser) send(method string, params any, session string) (int, error) {
	b.nextID++
	request := map[string]any{"id": b.nextID, "method": method, "params": params}
	if session != "" {
		request["sessionId"] = session
	}
	_ = b.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := b.conn.WriteJSON(request); err != nil {
		return 0, errors.New("the dedicated sign-in window is no longer available")
	}
	return b.nextID, nil
}

func (b *browser) read(ctx context.Context) (message, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = b.conn.SetReadDeadline(deadline)
	}
	var event message
	if err := b.conn.ReadJSON(&event); err != nil {
		return event, errors.New("sign-in was interrupted or timed out; open the sign-in window again")
	}
	if event.SessionID == b.session {
		b.observeDesigner(event)
		switch event.Method {
		case "Fetch.requestPaused":
			b.observePaused(event.Params)
		case "Page.frameNavigated":
			if b.callback == "" {
				b.observeNavigation(event.Params)
			}
		}
	}
	return event, nil
}

func (b *browser) observePaused(params json.RawMessage) {
	var paused struct {
		RequestID string `json:"requestId"`
		Request   struct {
			URL string `json:"url"`
		} `json:"request"`
	}
	if json.Unmarshal(params, &paused) != nil {
		return
	}
	if b.challenge.MatchesRedirect(paused.Request.URL) {
		b.callback = paused.Request.URL
		// Stop the portal from consuming our private PKCE flow.
		_, _ = b.send("Fetch.failRequest", map[string]any{"requestId": paused.RequestID, "errorReason": "Aborted"}, b.session)
	} else {
		_, _ = b.send("Fetch.continueRequest", map[string]any{"requestId": paused.RequestID}, b.session)
	}
}

func (b *browser) observeNavigation(params json.RawMessage) {
	var navigation struct {
		Frame struct {
			URL      string `json:"url"`
			Fragment string `json:"urlFragment"`
		} `json:"frame"`
	}
	if json.Unmarshal(params, &navigation) != nil {
		return
	}
	raw := navigation.Frame.URL
	if navigation.Frame.Fragment != "" && !strings.Contains(raw, "#") {
		raw += navigation.Frame.Fragment
	}
	if b.challenge.MatchesRedirect(raw) {
		b.callback = raw
		_, _ = b.send("Page.stopLoading", map[string]any{}, b.session)
	}
}

func (b *browser) call(ctx context.Context, method string, params any, session string) (json.RawMessage, error) {
	id, err := b.send(method, params, session)
	if err != nil {
		return nil, err
	}
	for {
		event, err := b.read(ctx)
		if err != nil {
			return nil, err
		}
		if event.ID == id {
			if len(event.Error) > 0 {
				return nil, errors.New("could not complete the browser sign-in operation in Edge")
			}
			return event.Result, nil
		}
	}
}

func splitCookies(cookies []auth.SSOCookie) (login, portal []auth.SSOCookie) {
	for _, cookie := range cookies {
		if cookie.Name == "" || cookie.Value == "" || strings.ContainsAny(cookie.Name+cookie.Value, "\r\n") {
			continue
		}
		domain := strings.TrimPrefix(strings.ToLower(cookie.Domain), ".")
		if loginCookie(domain, cookie.Name) {
			login = append(login, cookie)
		} else if portalCookie(domain) {
			portal = append(portal, cookie)
		}
	}
	return
}

func loginCookie(domain, name string) bool {
	return (domain == "login.microsoftonline.com" || domain == "microsoftonline.com") && (name == "ESTSAUTH" || name == "ESTSAUTHPERSISTENT")
}

func portalCookie(domain string) bool {
	switch domain {
	case "m365.cloud.microsoft", "cloud.microsoft", "microsoft.com":
		return true
	}
	return false
}

func Run(parent context.Context, config *models.Config, options Options) (result error) {
	unlock, err := acquireLoginLock()
	if err != nil {
		return err
	}
	defer unlock()
	defer func() {
		if result != nil {
			_ = writeStatus(options, "failed", result.Error())
		}
	}()
	if (config.TenantID == "") != (config.UserOID == "") {
		return errors.New("Microsoft account configuration is incomplete; restore both account IDs from this installation's backup before reconnecting")
	}
	if err := writeStatus(options, "starting", "Opening a dedicated Microsoft Edge sign-in window."); err != nil {
		return errors.New("could not write browser sign-in status")
	}
	if options.Timeout <= 0 {
		options.Timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, options.Timeout)
	defer cancel()
	return runSignIn(ctx, config, options)
}

func runSignIn(ctx context.Context, config *models.Config, options Options) error {
	tenant := config.TenantID
	firstLogin := tenant == "" && config.UserOID == ""
	if firstLogin {
		tenant = "organizations"
	}
	tm := auth.NewTokenManager(tenant, config.ClientID, config.Scope, "data/tokens/rt_90day.txt", "data/tokens/token_cache.json")
	tm.SetUserOID(config.UserOID)
	challenge, err := tm.BeginBrowserLogin()
	if err != nil {
		return err
	}
	b, err := startBrowser(ctx, challenge)
	if err != nil {
		return err
	}
	defer b.close()
	stopWatching := watchBrowserCancellation(ctx, b.conn)
	defer stopWatching()
	if err := b.attachSignIn(ctx); err != nil {
		return err
	}
	if err := b.waitForSignIn(ctx, options); err != nil {
		return err
	}
	login, portal, err := b.portalCookies(ctx, options)
	if err != nil {
		return err
	}
	if firstLogin {
		err = tm.CompleteBrowserSetup(ctx, challenge, b.callback, login, portal, func(tenant, oid string) error {
			if err := setup.SaveBrowserIdentity(tenant, oid); err != nil {
				return err
			}
			config.TenantID, config.UserOID = tenant, oid
			return nil
		})
	} else {
		err = tm.CompleteBrowserLogin(ctx, challenge, b.callback, login, portal)
	}
	if err != nil {
		return err
	}
	// A first sign-in discovers the identity. Use that verified identity for the
	// separate Designer exchange, never the temporary organizations tenant.
	designer := auth.NewTokenManager(config.TenantID, config.ClientID, config.Scope, "data/tokens/rt_90day.txt", "data/tokens/token_cache.json")
	designer.SetUserOID(config.UserOID)
	if err := b.completeDesigner(ctx, options, designer, config.TenantID); err != nil {
		return err
	}
	_ = writeStatus(options, "connected", "Microsoft text and Designer image credentials saved for this account. Automatic renewal is available while Microsoft permits it; reconnect here if interaction is required again.")
	return nil
}

func (b *browser) close() {
	defer b.conn.Close()
	// Keep the connection alive until Edge acknowledges the close request or
	// disconnects. Closing the socket immediately after sending can leave the
	// dedicated setup window open even though credential saving succeeded.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = b.call(ctx, "Browser.close", map[string]any{}, "")
}

func browserProfile() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", errors.New("could not locate the local browser profile directory")
	}
	profile := filepath.Join(cache, "M365Bridge", "BrowserSignIn")
	if err := os.MkdirAll(profile, 0700); err != nil {
		return "", errors.New("could not create the dedicated sign-in profile")
	}
	return profile, nil
}

func startBrowser(ctx context.Context, challenge *auth.BrowserChallenge) (*browser, error) {
	executable, err := edgeExecutable()
	if err != nil {
		return nil, err
	}
	profile, err := browserProfile()
	if err != nil {
		return nil, err
	}
	command := exec.Command(executable, "--user-data-dir="+profile, "--remote-debugging-port=0", "--remote-debugging-address=127.0.0.1", "--no-first-run", "--no-default-browser-check", "--new-window", "about:blank")
	if err := command.Start(); err != nil {
		return nil, errors.New("could not open Microsoft Edge")
	}
	go func() { _ = command.Wait() }()
	conn, err := connectBrowser(ctx, profile)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(4 << 20)
	return &browser{conn: conn, challenge: challenge}, nil
}

func connectBrowser(ctx context.Context, profile string) (*websocket.Conn, error) {
	var conn *websocket.Conn
	until := time.Now().Add(20 * time.Second)
	for conn == nil && time.Now().Before(until) && ctx.Err() == nil {
		if content, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort")); err == nil {
			if address, err := debuggerAddress(string(content)); err == nil {
				dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
				conn, _, _ = dialer.DialContext(ctx, address, nil)
			}
		}
		if conn == nil {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if conn == nil {
		return nil, errors.New("the dedicated Edge sign-in connection was unavailable; browser policy may prevent this connection")
	}
	return conn, nil
}

func watchBrowserCancellation(ctx context.Context, conn *websocket.Conn) func() {
	watchDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.UnderlyingConn().SetReadDeadline(time.Now())
		case <-watchDone:
		}
	}()
	return func() { close(watchDone) }
}

func (b *browser) signInTarget(ctx context.Context) (string, error) {
	data, err := b.call(ctx, "Target.getTargets", map[string]any{}, "")
	if err != nil {
		return "", err
	}
	var targets struct {
		TargetInfos []struct {
			ID   string `json:"targetId"`
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"targetInfos"`
	}
	_ = json.Unmarshal(data, &targets)
	targetID := ""
	for _, target := range targets.TargetInfos {
		if target.Type == "page" && target.URL == "about:blank" {
			targetID = target.ID
			break
		}
	}
	if targetID == "" {
		data, err = b.call(ctx, "Target.createTarget", map[string]any{"url": "about:blank"}, "")
		if err != nil {
			return "", err
		}
		var target struct {
			ID string `json:"targetId"`
		}
		_ = json.Unmarshal(data, &target)
		targetID = target.ID
	}
	return targetID, nil
}

func (b *browser) attachSignIn(ctx context.Context) error {
	targetID, err := b.signInTarget(ctx)
	if err != nil {
		return err
	}
	data, err := b.call(ctx, "Target.attachToTarget", map[string]any{"targetId": targetID, "flatten": true}, "")
	if err != nil {
		return err
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(data, &attached)
	b.session = attached.SessionID
	_, _ = b.call(ctx, "Target.activateTarget", map[string]any{"targetId": targetID}, "")
	_, err = b.call(ctx, "Page.enable", map[string]any{}, b.session)
	if err != nil {
		return err
	}
	_, err = b.call(ctx, "Fetch.enable", map[string]any{"patterns": []map[string]any{{"urlPattern": "https://m365.cloud.microsoft/spalanding*", "requestStage": "Request"}}}, b.session)
	return err
}

func (b *browser) waitForSignIn(ctx context.Context, options Options) error {
	_ = writeStatus(options, "waiting_for_sign_in", "Sign in to your configured Microsoft account in the Edge window. Complete MFA there if requested.")
	_, err := b.call(ctx, "Page.navigate", map[string]any{"url": b.challenge.AuthorizationURL}, b.session)
	if err != nil && b.callback == "" {
		return err
	}
	for b.callback == "" {
		if _, err := b.read(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (b *browser) portalCookies(ctx context.Context, options Options) ([]auth.SSOCookie, []auth.SSOCookie, error) {
	_ = writeStatus(options, "saving", "Verifying the Microsoft sign-in and saving renewed credentials.")
	_, _ = b.call(ctx, "Fetch.disable", map[string]any{}, b.session)
	// Opening the portal establishes its own browser cookies for sidebar and
	// conversation operations. The OAuth code and verifier remain only in memory.
	_, _ = b.call(ctx, "Page.navigate", map[string]any{"url": "https://m365.cloud.microsoft/"}, b.session)
	select {
	case <-ctx.Done():
		return nil, nil, errors.New("sign-in timed out before credentials could be saved")
	case <-time.After(5 * time.Second):
	}
	data, err := b.call(ctx, "Network.getCookies", map[string]any{"urls": []string{b.challenge.AuthorizationURL, "https://m365.cloud.microsoft/", "https://m365.cloud.microsoft/chat/"}}, b.session)
	if err != nil {
		return nil, nil, err
	}
	var cookies struct {
		Cookies []auth.SSOCookie `json:"cookies"`
	}
	if json.Unmarshal(data, &cookies) != nil {
		return nil, nil, errors.New("the browser session could not be read")
	}
	login, portal := splitCookies(cookies.Cookies)
	return login, portal, nil
}
