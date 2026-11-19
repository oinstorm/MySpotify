package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vinyl/internal/spotify"
)

// A device ID survives restarts and a malformed file is not silently replaced with a new device.
func TestDeviceIDPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device-id")
	first, err := loadDeviceID(path)
	if err != nil || len(first) != 32 {
		t.Fatalf("could not create a device ID: %v", err)
	}
	if got, err := loadDeviceID(path); err != nil || got != first {
		t.Fatalf("device ID did not survive reload: %v", err)
	}
	if err := os.WriteFile(path, []byte("malformed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDeviceID(path); err == nil {
		t.Fatal("silently replaced a malformed device ID")
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "malformed" {
		t.Fatal("changed an unreadable saved device ID")
	}
}

// useDeviceTokenStore isolates secure persistence and all account requests from the real application.
func useDeviceTokenStore(t *testing.T) (string, *int) {
	t.Helper()
	dir := filepath.Dir(useCookieStore(t))
	oldDir, oldClient := deviceIdentityDir, spotify.Client
	deviceIdentityDir = func() (string, error) { return dir, nil }
	asks := 0
	spotify.Client = &http.Client{Transport: connectTransport(func(r *http.Request) (*http.Response, error) {
		var body string
		switch r.URL.Path {
		case "/api/server-time":
			body = fmt.Sprintf(`{"serverTime":%d}`, time.Now().Unix())
		case "/api/token":
			body = fmt.Sprintf(`{"clientId":"test-client","accessToken":"test-access","accessTokenExpirationTimestampMs":%d}`, time.Now().Add(time.Hour).UnixMilli())
		case "/v1/clienttoken":
			asks++
			body = `{"granted_token":{"token":"test-device-secret","expires_after_seconds":7200,"refresh_after_seconds":3600}}`
		default:
			return nil, fmt.Errorf("unexpected network request to %s", r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	t.Cleanup(func() {
		deviceIdentityDir, spotify.Client = oldDir, oldClient
		if err := spotify.ConfigureClientTokens(strings.Repeat("f", 32), nil, nil); err != nil {
			t.Error(err)
		}
	})
	return dir, &asks
}

// Securely saved device credentials survive a restart without exposing the token or reissuing it.
func TestDeviceCredentialsAreSealedAndRestored(t *testing.T) {
	dir, asks := useDeviceTokenStore(t)
	if err := configureClientTokens(); err != nil {
		t.Fatal(err)
	}
	if _, err := spotify.AccountIdentity("test-cookie"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "device-credentials")
	sealed, err := os.ReadFile(path)
	if err != nil || bytes.Contains(sealed, []byte("test-device-secret")) {
		t.Fatalf("credential was not securely stored: %v", err)
	}
	if err := configureClientTokens(); err != nil {
		t.Fatal(err)
	}
	if _, err := spotify.AccountIdentity("test-cookie"); err != nil || *asks != 1 {
		t.Fatalf("restart reissued a valid device credential: %v, asks=%d", err, *asks)
	}
	if err := os.WriteFile(path, []byte("unreadable"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "device-id"))
	if err != nil {
		t.Fatal(err)
	}
	if err := configureClientTokens(); err == nil {
		t.Fatal("did not report an unreadable credential cache")
	}
	if _, err := spotify.AccountIdentity("test-cookie"); err != nil || *asks != 2 {
		t.Fatalf("could not recover an unreadable credential cache: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "device-id"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("device ID changed while recovering credentials")
	}
}

// If secure storage is unavailable, the app keeps credentials only in memory and writes no plaintext fallback.
func TestDeviceCredentialsNeverFallBackToPlaintext(t *testing.T) {
	dir, asks := useDeviceTokenStore(t)
	sealCookies = func([]byte) ([]byte, error) { return nil, errors.New("key store unavailable") }
	if err := configureClientTokens(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := spotify.AccountIdentity("test-cookie"); err != nil {
			t.Fatal(err)
		}
	}
	if *asks != 1 {
		t.Fatal("reissued credentials because secure persistence failed")
	}
	if _, err := os.Stat(filepath.Join(dir, "device-credentials")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wrote a credential file without encryption: %v", err)
	}
}
