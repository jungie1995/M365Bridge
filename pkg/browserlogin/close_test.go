package browserlogin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestCloseWaitsForBrowserAcknowledgement(t *testing.T) {
	received := make(chan message, 1)
	acknowledge := make(chan struct{})
	disconnected := make(chan struct{})
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var request message
		if conn.ReadJSON(&request) != nil {
			return
		}
		received <- request
		<-acknowledge
		_ = conn.WriteJSON(map[string]any{"id": request.ID, "result": map[string]any{}})
		_, _, _ = conn.ReadMessage()
		close(disconnected)
	}))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	b := &browser{conn: conn}
	done := make(chan struct{})
	go func() { b.close(); close(done) }()
	request := <-received
	if request.Method != "Browser.close" || request.SessionID != "" {
		close(acknowledge)
		t.Fatal("cleanup did not request closing the dedicated browser")
	}
	select {
	case <-done:
		close(acknowledge)
		t.Fatal("cleanup disconnected before the browser acknowledged closing")
	case <-time.After(100 * time.Millisecond):
	}
	close(acknowledge)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not finish after the browser acknowledged closing")
	}
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("cleanup left its control socket open")
	}
}

func TestCloseToleratesBrowserDisconnect(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var request message
		_ = conn.ReadJSON(&request)
	}))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	b := &browser{conn: conn}
	done := make(chan struct{})
	go func() { b.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not accept the browser closing its own connection")
	}
}

func TestCloseIsBoundedWhenBrowserDoesNotRespond(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var request message
		_ = conn.ReadJSON(&request)
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	b := &browser{conn: conn}
	done := make(chan struct{})
	go func() { b.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("an unresponsive browser blocked sign-in cleanup")
	}
}
