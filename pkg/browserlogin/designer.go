package browserlogin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/auth"
)

type designerRequest struct {
	url   string
	ready bool
}
type designerObserver struct {
	tenant   string
	requests map[string]designerRequest
	bodies   map[int]bool
	save     func([]byte) error
	done     bool
	err      error
	notify   func(string)
	matched  int
}

type designerNetworkEvent struct {
	RequestID string `json:"requestId"`
	Request   struct {
		URL      string `json:"url"`
		Method   string `json:"method"`
		PostData string `json:"postData"`
	} `json:"request"`
	Response struct {
		URL    string `json:"url"`
		Status int    `json:"status"`
	} `json:"response"`
}

func (b *browser) observeDesigner(event message) {
	d := b.designer
	if d == nil || d.done || event.SessionID != b.session {
		return
	}
	if d.bodies[event.ID] {
		d.saveBody(event)
		return
	}
	var params designerNetworkEvent
	if json.Unmarshal(event.Params, &params) != nil {
		return
	}
	switch event.Method {
	case "Network.requestWillBeSent":
		d.observeRequest(params)
	case "Network.responseReceived":
		d.observeResponse(params)
	case "Network.loadingFailed":
		delete(d.requests, params.RequestID)
	case "Network.loadingFinished":
		b.readDesignerBody(params.RequestID)
	}
}

func (d *designerObserver) observeResponse(params designerNetworkEvent) {
	if request, ok := d.requests[params.RequestID]; ok {
		request.ready = params.Response.URL == request.url && params.Response.Status == 200
		d.requests[params.RequestID] = request
	}
}

func (d *designerObserver) saveBody(event message) {
	delete(d.bodies, event.ID)
	var result struct {
		Body    string `json:"body"`
		Encoded bool   `json:"base64Encoded"`
	}
	if len(event.Error) > 0 || json.Unmarshal(event.Result, &result) != nil || len(result.Body) > 360<<10 {
		return
	}
	data := []byte(result.Body)
	if result.Encoded {
		var err error
		data, err = base64.StdEncoding.DecodeString(result.Body)
		if err != nil {
			return
		}
	}
	d.err = d.save(data)
	d.done = d.err == nil
	if d.notify != nil && d.err != nil {
		d.notify(d.err.Error())
	}
}

func (d *designerObserver) observeRequest(params designerNetworkEvent) {
	// A redirect may reuse the request ID. Forget it unless the new request
	// independently satisfies the exact host, client and scope restrictions.
	delete(d.requests, params.RequestID)
	if len(d.requests) >= 16 || !auth.IsDesignerBrowserRequest(params.Request.URL, params.Request.Method, params.Request.PostData, d.tenant) {
		return
	}
	d.requests[params.RequestID] = designerRequest{url: params.Request.URL}
	d.matched++
	if d.notify != nil {
		d.notify("Microsoft Designer authorization started. Waiting for the same-account renewable image credential.")
	}
}

func (b *browser) readDesignerBody(requestID string) {
	d := b.designer
	request, ok := d.requests[requestID]
	delete(d.requests, requestID)
	if !ok || !request.ready || len(d.bodies) >= 16 {
		return
	}
	id, err := b.send("Network.getResponseBody", map[string]any{"requestId": requestID}, b.session)
	if err == nil {
		d.bodies[id] = true
	}
}

// completeDesigner runs the actual Copilot web flow in the dedicated profile.
// It submits only this disclosed setup prompt, never private project content.
// Passwords, consent and MFA remain interactive Microsoft operations.
func (b *browser) completeDesigner(ctx context.Context, options Options, tm *auth.TokenManager, tenant string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_ = writeStatus(options, "saving", "Text sign-in saved. Checking the separate Microsoft Designer image authorization.")
	if tm.RenewSavedDesigner(ctx) {
		return nil
	}
	b.designer = &designerObserver{tenant: tenant, requests: map[string]designerRequest{}, bodies: map[int]bool{}, save: func(data []byte) error { return tm.SaveDesignerBrowserCredentials(ctx, data) }}
	b.designer.notify = func(message string) { _ = writeStatus(options, "saving", message) }
	defer func() { b.designer = nil }()
	_ = writeStatus(options, "saving", "Text sign-in saved. Authorizing Designer images in Microsoft Copilot; setup will submit one small apple image check. Complete any Microsoft prompt in Edge.")
	if err := b.openDesignerComposer(ctx); err != nil {
		return err
	}
	if err := b.waitForDesignerComposer(ctx); err != nil {
		return err
	}
	if !b.designer.done {
		if err := b.submitDesignerPrompt(ctx); err != nil {
			return err
		}
	}
	return b.waitForDesignerAuthorization(ctx)
}

func (b *browser) openDesignerComposer(ctx context.Context) error {
	if _, err := b.call(ctx, "Network.enable", map[string]any{"maxPostDataSize": 128 << 10}, b.session); err != nil {
		return err
	}
	if _, err := b.call(ctx, "Runtime.enable", map[string]any{}, b.session); err != nil {
		return err
	}
	if _, err := b.call(ctx, "Page.navigate", map[string]any{"url": "https://m365.cloud.microsoft/chat"}, b.session); err != nil {
		return err
	}
	return nil
}

func (b *browser) waitForDesignerComposer(ctx context.Context) error {
	// Short evaluations tolerate portal redirects replacing the page context.
	// Each call drains network events through the same scoped observer.
	expression := `(() => { if(location.hostname!=='m365.cloud.microsoft') return false; const es=[...document.querySelectorAll('textarea, [contenteditable="true"][role="textbox"], [contenteditable="true"][data-placeholder]')]; const e=es.find(e=>e.getClientRects().length && !e.disabled); if(!e) return false; e.focus(); return true; })()`
	for until := time.Now().Add(90 * time.Second); time.Now().Before(until); {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := b.call(ctx, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true}, b.session)
		var ready struct {
			Result struct {
				Value bool `json:"value"`
			} `json:"result"`
		}
		if err == nil && json.Unmarshal(data, &ready) == nil && ready.Result.Value {
			return ctx.Err()
		}
		if b.designer.done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("text sign-in was saved, but the Copilot image composer was unavailable; check this account's Copilot access and reconnect")
}

func (b *browser) submitDesignerPrompt(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := b.call(ctx, "Input.insertText", map[string]any{"text": "Generate an image of a single red apple on a plain white background."}, b.session); err != nil {
		return err
	}
	for _, eventType := range []string{"keyDown", "keyUp"} {
		if _, err := b.call(ctx, "Input.dispatchKeyEvent", map[string]any{"type": eventType, "key": "Enter", "code": "Enter", "windowsVirtualKeyCode": 13}, b.session); err != nil {
			return err
		}
	}
	b.designer.notify("The setup image prompt was submitted in Copilot. Waiting for Microsoft Designer authorization; leave the browser open.")
	return nil
}

func (b *browser) waitForDesignerAuthorization(ctx context.Context) error {
	for !b.designer.done {
		if _, err := b.read(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if b.designer.err != nil {
				return b.designer.err
			}
			return errors.New("text sign-in was saved, but Designer image authorization was not confirmed; reconnect in Edge and check image entitlement or Microsoft consent. Image jobs are not ready")
		}
	}
	return nil
}
