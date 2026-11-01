package browserlogin

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/auth"
	"github.com/gorilla/websocket"
)

func TestDesignerCancellationDoesNotSubmitSetupPrompt(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(fmt.Sprintf("composer-ready-%t", ready), func(t *testing.T) {
			testDesignerCancellation(t, ready)
		})
	}
}

func testDesignerCancellation(t *testing.T, ready bool) {
	t.Helper()
	t.Chdir(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var submitted atomic.Bool
	b := testBrowserConnection(t, func(conn *websocket.Conn) {
		for {
			var request message
			if conn.ReadJSON(&request) != nil {
				return
			}
			if request.Method == "Input.insertText" {
				submitted.Store(true)
				return
			}
			if request.Method == "Runtime.evaluate" {
				cancel()
			}
			if conn.WriteJSON(map[string]any{"id": request.ID, "sessionId": request.SessionID, "result": map[string]any{"result": map[string]bool{"value": ready}}}) != nil {
				return
			}
		}
	})
	tm := auth.NewTokenManager("tenant", "client", "scope", "refresh", "cache")
	err := b.completeDesigner(ctx, Options{}, tm, "tenant")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled sign-in returned %v, want context.Canceled", err)
	}
	if submitted.Load() || b.designer != nil {
		t.Fatal("cancelled sign-in submitted a prompt or retained its observer")
	}
}
