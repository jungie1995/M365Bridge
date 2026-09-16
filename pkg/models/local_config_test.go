package models

import (
	"os"
	"testing"
)

func TestManagedInstallationDoesNotInheritAnotherMachinesCredentials(t *testing.T) {
	t.Chdir(t.TempDir())
	_ = os.MkdirAll("data", 0700)
	_ = os.WriteFile("data/.env", []byte("M365_TENANT_ID=local-tenant\nM365_USER_OID=local-user\nM365_API_KEY='local-key'\n"), 0600)
	for _, key := range []string{"M365_TENANT_ID", "M365_USER_OID", "M365_API_KEY", "M365_API_KEYS", "M365_CLIENT_ID"} {
		t.Setenv(key, "stale")
	}
	t.Setenv("M365_CONFIG_FROM_INSTALLATION", "1")
	t.Setenv("M365_BROWSER_IMAGE_ROUTING", "1")
	config := LoadConfig()
	if config.TenantID != "local-tenant" || config.UserOID != "local-user" || len(config.APIKeys) != 1 || config.APIKeys[0] != "local-key" || config.ClientID != DefaultClientID {
		t.Fatal("stale credentials survived managed startup")
	}
	if os.Getenv("M365_BROWSER_IMAGE_ROUTING") != "1" {
		t.Fatal("image mode was erased")
	}
}
