package auth

import "testing"

func TestRefactoredSSORequestKeepsLoginCookiesScoped(t *testing.T) {
	for target, want := range map[string]bool{
		"https://login.microsoftonline.com/tenant/oauth2/v2.0/authorize": true,
		"https://m365.cloud.microsoft/":                                  false,
		"http://login.microsoftonline.com/":                              false,
		"https://login.microsoftonline.com.example.org/":                 false,
	} {
		req, err := ssoBrowserRequest(target, "ESTSAUTH=synthetic-fixture")
		if err != nil {
			t.Fatal(err)
		}
		if got := req.Header.Get("Cookie") != ""; got != want {
			t.Fatalf("cookie scope incorrect for %s", target)
		}
	}
}
