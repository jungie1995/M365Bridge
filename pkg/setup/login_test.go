package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/M365Bridge/pkg/auth"
	"github.com/KilimcininKorOglu/M365Bridge/pkg/models"
)

func TestPrintDeviceBannerQuiet(t *testing.T) {
	dc := &auth.DeviceCode{
		UserCode:        "TEST-1234",
		VerificationURI: "https://login.microsoft.com/device",
		Interval:        5,
	}
	// Quiet banner should not panic or produce errors
	printDeviceBanner(true, dc)
}

func TestPrintPKCEBannerQuiet(t *testing.T) {
	printPKCEBanner(true, "https://login.microsoftonline.com/authorize")
}

func TestPrintNativeSuccess(t *testing.T) {
	res := &auth.TokenResult{
		TenantID: "tenant-abc",
		UserOID:  "oid-def",
		Email:    "test@example.com",
	}
	// Should not panic
	printNativeSuccess(res)
}

func TestSaveCredentialStores(t *testing.T) {
	tmpDir := t.TempDir()
	refreshFile := filepath.Join(tmpDir, "rt_90day.txt")
	cacheFile := filepath.Join(tmpDir, "cache.json")
	envFile := filepath.Join(tmpDir, ".env")

	res := &auth.TokenResult{
		AccessToken:  "mock-access-token",
		RefreshToken: "mock-refresh-token",
		TenantID:     "tenant-test-id",
		UserOID:      "user-test-oid",
		ExpiresAt:    time.Now().Add(time.Hour),
	}

	if err := saveCredentialStoresTo(res, models.DefaultDeviceClientID, refreshFile, cacheFile, envFile); err != nil {
		t.Fatalf("saveCredentialStoresTo failed: %v", err)
	}

	// Verify rt_90day.txt exists and is non-empty
	if data, err := os.ReadFile(refreshFile); err != nil || len(data) == 0 {
		t.Errorf("expected refresh token file to be written, err=%v len=%d", err, len(data))
	}

	// Verify cache.json exists
	if data, err := os.ReadFile(cacheFile); err != nil || len(data) == 0 {
		t.Errorf("expected cache file to be written, err=%v len=%d", err, len(data))
	}

	// Verify .env exists and contains the client ID
	if data, err := os.ReadFile(envFile); err != nil {
		t.Errorf("expected .env file to be written, err=%v", err)
	} else if len(data) == 0 {
		t.Error("expected .env file to be non-empty")
	}
}

func TestNativeLoginPreservesOperatorSettings(t *testing.T) {
	for _, clientID := range []string{models.DefaultDeviceClientID, models.DefaultClientID} {
		t.Run(clientID, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if err := os.MkdirAll("data/tokens", 0700); err != nil {
				t.Fatal(err)
			}
			settings := []string{
				"# operator settings",
				"M365_API_KEY='fixture-private-key'",
				`M365_API_KEYS="first-fixture,second-fixture"`,
				`M365_WEB_UI_PASSWORD="fixture-password"`,
				"M365_MAX_TOOL_ROUNDS=73",
				"M365_ENABLE_CODE_TOOLS=0",
				"CUSTOM=keep=this=value",
			}
			before := strings.Join(settings, "\r\n") + "\r\nM365_TENANT_ID=old\r\nM365_TENANT_ID=duplicate\r\nM365_USER_OID=old\r\nM365_CLIENT_ID=old\r\nM365_CLIENT_ID=duplicate\r\n"
			if err := os.WriteFile(defaultEnvFile, []byte(before), 0600); err != nil {
				t.Fatal(err)
			}
			result := &auth.TokenResult{AccessToken: "fixture-access", RefreshToken: "fixture-refresh", TenantID: "verified-tenant", UserOID: "verified-user", ExpiresAt: time.Now().Add(time.Hour)}
			if err := saveCredentialStoresTo(result, clientID, defaultRefreshTokenFile, defaultCacheFile, defaultEnvFile); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(defaultEnvFile)
			if err != nil {
				t.Fatal(err)
			}
			assertNativeLoginSettings(t, settings, data, clientID)
			if err := saveEnvTo(result.TenantID, result.UserOID, clientID, defaultEnvFile); err != nil {
				t.Fatal(err)
			}
			repeated, err := os.ReadFile(defaultEnvFile)
			if err != nil || string(repeated) != string(data) {
				t.Fatal("repeated native login changed preserved settings")
			}
		})
	}
}

func assertNativeLoginSettings(t *testing.T, settings []string, data []byte, clientID string) {
	t.Helper()
	for _, setting := range append(settings, "M365_TENANT_ID=verified-tenant", "M365_USER_OID=verified-user", "M365_CLIENT_ID="+clientID) {
		if !strings.Contains(string(data), setting+"\n") {
			t.Errorf("native login discarded setting %q", setting)
		}
	}
	for _, key := range []string{"M365_TENANT_ID=", "M365_USER_OID=", "M365_CLIENT_ID="} {
		if strings.Count(string(data), key) != 1 {
			t.Errorf("native login retained duplicate identity key %q", key)
		}
	}
}

func TestNativeLoginRefusesUnreadableExistingSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveEnvTo("tenant", "user", models.DefaultDeviceClientID, path); err == nil {
		t.Fatal("native login ignored an unreadable existing settings file")
	}
}
