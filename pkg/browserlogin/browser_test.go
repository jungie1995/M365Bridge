package browserlogin

import (
	"github.com/KilimcininKorOglu/M365Bridge/pkg/auth"
	"strings"
	"testing"
)

func TestDedicatedBrowserEndpointIsLoopbackOnly(t *testing.T) {
	address, err := debuggerAddress("12345\n/devtools/browser/test-id\n")
	if err != nil || !strings.HasPrefix(address, "ws://127.0.0.1:12345/") {
		t.Fatal("wrong browser endpoint")
	}
	for _, value := range []string{"80\n/devtools/browser/id", "12345\nwss://remote.example/", "12345\n/devtools/browser/id?remote=1"} {
		if _, err := debuggerAddress(value); err == nil {
			t.Fatal("unsafe endpoint accepted")
		}
	}
}

func TestOnlyRequiredMicrosoftSessionCookiesAreSelected(t *testing.T) {
	login, web := splitCookies([]auth.SSOCookie{
		{Name: "ESTSAUTH", Value: "login", Domain: ".login.microsoftonline.com"},
		{Name: "ESTSAUTHPERSISTENT", Value: "persist", Domain: "login.microsoftonline.com"},
		{Name: "telemetry", Value: "unused", Domain: "login.microsoftonline.com"},
		{Name: "portal", Value: "web", Domain: "m365.cloud.microsoft"},
		{Name: "session", Value: "private", Domain: "unrelated.example"},
	})
	if len(login) != 2 || len(web) != 1 {
		t.Fatal("cookie selection exceeded the intended Microsoft session scope")
	}
}
