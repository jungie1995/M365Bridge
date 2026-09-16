package browserlogin

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func designerEvent(session, method string, params any) message {
	raw, _ := json.Marshal(params)
	return message{SessionID: session, Method: method, Params: raw}
}

func TestDesignerObserverIgnoresForeignSessionsAndRedirects(t *testing.T) {
	b := &browser{session: "own", designer: &designerObserver{tenant: "tenant", requests: map[string]designerRequest{}, bodies: map[int]bool{}}}
	params := map[string]any{"requestId": "1", "request": map[string]string{"url": "https://login.microsoftonline.com/tenant/oauth2/v2.0/token?brk_client_id=4765445b-32c6-49b0-83e6-1d93765276ca", "method": "POST", "postData": "client_id=c0ab8ce9-e9a0-42e7-b064-33d422df41f1&scope=https%3A%2F%2Fdesignerappservice.officeapps.live.com%2F.default"}}
	b.observeDesigner(designerEvent("other", "Network.requestWillBeSent", params))
	if len(b.designer.requests) != 0 {
		t.Fatal("foreign tab observed")
	}
	b.observeDesigner(designerEvent("own", "Network.requestWillBeSent", params))
	if len(b.designer.requests) != 1 {
		t.Fatal("own Designer request not tracked")
	}
	b.observeDesigner(designerEvent("own", "Network.requestWillBeSent", map[string]any{"requestId": "1", "request": map[string]string{"url": "https://attacker.example/", "method": "POST"}}))
	if len(b.designer.requests) != 0 {
		t.Fatal("redirect retained authorization")
	}
}

func TestDesignerObserverHandlesValidatedBodyOnceWithoutLoggingIt(t *testing.T) {
	calls := 0
	b := &browser{session: "own", designer: &designerObserver{bodies: map[int]bool{42: true}, save: func(data []byte) error {
		calls++
		if string(data) != "fixture" {
			return errors.New("invalid")
		}
		return nil
	}}}
	raw, _ := json.Marshal(map[string]any{"body": "Zml4dHVyZQ==", "base64Encoded": true})
	event := message{ID: 42, SessionID: "other", Result: raw}
	b.observeDesigner(event)
	if calls != 0 {
		t.Fatal("foreign response accepted")
	}
	event.SessionID = "own"
	b.observeDesigner(event)
	b.observeDesigner(event)
	if calls != 1 || !b.designer.done {
		t.Fatal("validated response was not consumed once")
	}
}

func TestDesignerObserverRejectsOversizedOrFailedBodies(t *testing.T) {
	for _, result := range []string{`{"body":"???","base64Encoded":true}`, `{"body":"` + strings.Repeat("x", 361<<10) + `"}`} {
		b := &browser{session: "own", designer: &designerObserver{bodies: map[int]bool{42: true}, save: func([]byte) error { t.Fatal("unsafe body passed to persistence"); return nil }}}
		b.observeDesigner(message{ID: 42, SessionID: "own", Result: json.RawMessage(result)})
		if b.designer.done {
			t.Fatal("invalid response reported success")
		}
	}
}
