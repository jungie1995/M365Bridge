package setup

import (
	"os"
	"strings"
	"testing"
)

func TestSetupPreservesOperatorSettings(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("data", 0700); err != nil {
		t.Fatal(err)
	}
	before := "# local settings\nM365_API_KEY=synthetic-fixture\nM365_MAX_TOOL_ROUNDS=128\nCUSTOM=keep=this\nM365_TENANT_ID=old\nM365_TENANT_ID=duplicate\n"
	if err := os.WriteFile(defaultEnvFile, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveEnv("new-tenant", "new-user"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(defaultEnvFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, setting := range []string{"M365_API_KEY=synthetic-fixture", "M365_MAX_TOOL_ROUNDS=128", "CUSTOM=keep=this", "M365_TENANT_ID=new-tenant", "M365_USER_OID=new-user"} {
		if !strings.Contains(string(data), setting) {
			t.Fatal("setup discarded an operator setting or identity update")
		}
	}
	if strings.Count(string(data), "M365_TENANT_ID=") != 1 {
		t.Fatal("duplicate stale identity survived")
	}
}
