package spotify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// useClientTokens isolates the device cache and sends all device requests to the test's server.
func useClientTokens(t *testing.T, handle http.HandlerFunc) *clientTokenCache {
	t.Helper()
	srv := httptest.NewServer(handle)
	oldURL, oldCache := clientTokenURL, clientTokens
	c := newClientTokenCache()
	clientTokenURL, clientTokens = srv.URL, c
	t.Cleanup(func() { srv.Close(); clientTokenURL, clientTokens = oldURL, oldCache })
	return c
}

// Renewing device credentials keeps the same device and names the running platform, not macOS on Windows.
func TestClientTokenKeepsDeviceAndPlatform(t *testing.T) {
	var devices []string
	c := useClientTokens(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Client struct {
				ID  string `json:"client_id"`
				SDK struct {
					Device  string `json:"device_id"`
					OS      string `json:"os"`
					Version string `json:"os_version"`
				} `json:"js_sdk_data"`
			} `json:"client_data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		platform, version := clientPlatform()
		if body.Client.SDK.OS != platform || body.Client.SDK.Version != version || r.Header.Get("User-Agent") != tokenUserAgent {
			t.Error("device request used a different platform or browser identity")
		}
		if runtime.GOOS == "windows" && (platform != "windows" || version == "10.15.7") {
			t.Error("Windows device request still claims macOS")
		}
		devices = append(devices, body.Client.SDK.Device)
		fmt.Fprint(w, `{"granted_token":{"token":"device","expires_after_seconds":120,"refresh_after_seconds":60}}`)
	})
	now := time.Unix(1700000000, 0)
	c.now = func() time.Time { return now }
	for range 3 {
		if _, err := c.get("client"); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	if len(devices) != 3 || len(devices[0]) != 32 || devices[0] != devices[1] || devices[1] != devices[2] {
		t.Fatal("device identity changed during credential refresh")
	}
}

// The server's refresh deadline, not each access-token renewal, decides when a device request is sent.
func TestClientTokenRefreshDeadline(t *testing.T) {
	var asks atomic.Int32
	c := useClientTokens(t, func(w http.ResponseWriter, r *http.Request) {
		n := asks.Add(1)
		fmt.Fprintf(w, `{"granted_token":{"token":"device-%d","expires_after_seconds":120,"refresh_after_seconds":60}}`, n)
	})
	now := time.Unix(1700000000, 0)
	c.now = func() time.Time { return now }
	first, err := c.get("client")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(59 * time.Second)
	if got, err := c.get("client"); err != nil || got != first || asks.Load() != 1 {
		t.Fatalf("renewed before the deadline: %v, asks=%d", err, asks.Load())
	}
	now = now.Add(time.Second)
	if got, err := c.get("client"); err != nil || got == first || asks.Load() != 2 {
		t.Fatalf("did not refresh at the deadline: %v, asks=%d", err, asks.Load())
	}
}

// Concurrent users share one device request instead of all replacing the credential at once.
func TestClientTokenConcurrentRefresh(t *testing.T) {
	var asks atomic.Int32
	c := useClientTokens(t, func(w http.ResponseWriter, r *http.Request) {
		asks.Add(1)
		fmt.Fprint(w, `{"granted_token":{"token":"shared","expires_after_seconds":120,"refresh_after_seconds":60}}`)
	})
	c.now = func() time.Time { return time.Unix(1700000000, 0) }
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if got, err := c.get("client"); err != nil || got != "shared" {
				t.Errorf("device request failed: %v", err)
			}
		})
	}
	wg.Wait()
	if got := asks.Load(); got != 1 {
		t.Fatalf("sent %d concurrent device requests, want 1", got)
	}
}

// A refused refresh observes Retry-After and never returns the old credential after its expiry.
func TestClientTokenFailedRefreshBacksOff(t *testing.T) {
	var asks atomic.Int32
	c := useClientTokens(t, func(w http.ResponseWriter, r *http.Request) {
		if asks.Add(1) == 1 {
			fmt.Fprint(w, `{"granted_token":{"token":"kept","expires_after_seconds":120,"refresh_after_seconds":60}}`)
			return
		}
		w.Header().Set("Retry-After", "300")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	now := time.Unix(1700000000, 0)
	c.now = func() time.Time { return now }
	if _, err := c.get("client"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	for range 3 {
		if got, err := c.get("client"); err != nil || got != "kept" {
			t.Fatalf("lost a valid credential during refresh failure: %v", err)
		}
	}
	now = now.Add(time.Minute)
	if got, err := c.get("client"); err == nil || got != "" || asks.Load() != 2 {
		t.Fatalf("used an expired credential or retried before Retry-After: %v, asks=%d", err, asks.Load())
	}
	now = now.Add(4 * time.Minute)
	if _, err := c.get("client"); err == nil || asks.Load() != 3 {
		t.Fatalf("did not retry after Retry-After: %v, asks=%d", err, asks.Load())
	}
}

// Restarts reuse only credentials belonging to the same installation, client ID, build and platform.
func TestClientTokenRestoresOnlyMatchingCache(t *testing.T) {
	var asks atomic.Int32
	c := useClientTokens(t, func(w http.ResponseWriter, r *http.Request) {
		asks.Add(1)
		fmt.Fprint(w, `{"granted_token":{"token":"kept","expires_after_seconds":7200,"refresh_after_seconds":3600}}`)
	})
	c.now = func() time.Time { return time.Unix(1700000000, 0) }
	device := strings.Repeat("a", 32)
	var saved []byte
	if err := ConfigureClientTokens(device, nil, func(b []byte) error { saved = bytes.Clone(b); return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := c.get("client"); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureClientTokens(device, saved, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.get("client"); err != nil || asks.Load() != 1 {
		t.Fatalf("did not reuse the saved credential: %v", err)
	}
	if _, err := c.get("other-client"); err != nil || asks.Load() != 2 {
		t.Fatalf("reused another client's credential: %v", err)
	}
	var file clientTokenFile
	if err := json.Unmarshal(saved, &file); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*clientTokenFile){
		func(f *clientTokenFile) { f.Device = strings.Repeat("b", 32) },
		func(f *clientTokenFile) { f.Version = "other-build" },
		func(f *clientTokenFile) { f.Platform = "other-platform" },
	} {
		changed := file
		change(&changed)
		data, err := json.Marshal(changed)
		if err != nil {
			t.Fatal(err)
		}
		if err := ConfigureClientTokens(device, data, nil); err != nil {
			t.Fatal(err)
		}
		before := asks.Load()
		if _, err := c.get("client"); err != nil || asks.Load() != before+1 {
			t.Fatalf("reused an incompatible credential: %v", err)
		}
	}
}

// Missing or invalid expiry metadata never makes an unbounded reusable credential.
func TestClientTokenWithoutExpiryIsNotCached(t *testing.T) {
	var asks atomic.Int32
	c := useClientTokens(t, func(w http.ResponseWriter, r *http.Request) {
		asks.Add(1)
		fmt.Fprint(w, `{"granted_token":{"token":"once"}}`)
	})
	for range 2 {
		if _, err := c.get("client"); err != nil {
			t.Fatal(err)
		}
	}
	if asks.Load() != 2 {
		t.Fatal("cached a credential with no known expiry")
	}
}

// Invalid or already elapsed lifetimes never return an expired credential, even once.
func TestClientTokenRejectsExpiredResponses(t *testing.T) {
	for _, seconds := range []int64{0, -1, 1<<63 - 1, 1} {
		t.Run(fmt.Sprint(seconds), func(t *testing.T) {
			var clock atomic.Int64
			clock.Store(1700000000)
			c := useClientTokens(t, func(w http.ResponseWriter, r *http.Request) {
				clock.Add(2)
				fmt.Fprintf(w, `{"granted_token":{"token":"expired","expires_after_seconds":%d}}`, seconds)
			})
			c.now = func() time.Time { return time.Unix(clock.Load(), 0) }
			if got, err := c.get("client"); err == nil || got != "" {
				t.Fatal("returned a credential with invalid or elapsed expiry")
			}
		})
	}
}

// An expired or corrupt persisted credential is discarded rather than silently gaining a new lifetime.
func TestClientTokenRejectsExpiredOrCorruptCache(t *testing.T) {
	var asks atomic.Int32
	c := useClientTokens(t, func(w http.ResponseWriter, r *http.Request) {
		asks.Add(1)
		fmt.Fprint(w, `{"granted_token":{"token":"fresh","expires_after_seconds":120}}`)
	})
	now := time.Unix(1700000000, 0)
	c.now = func() time.Time { return now }
	platform, version := clientPlatform()
	device := strings.Repeat("a", 32)
	data, err := json.Marshal(clientTokenFile{
		Device: device, Version: clientVersion, Platform: platform + "/" + version,
		Tokens: map[string]cachedClientToken{"client": {Token: "expired", Expires: now, Refresh: now.Add(-time.Minute)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ConfigureClientTokens(device, data, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := c.get("client"); err != nil || got != "fresh" || asks.Load() != 1 {
		t.Fatalf("did not replace expired cached credentials: %v", err)
	}
	if err := ConfigureClientTokens(device, []byte("corrupt"), nil); err == nil {
		t.Fatal("accepted corrupt credential cache")
	}
	if got, err := c.get("client"); err != nil || got != "fresh" || asks.Load() != 2 {
		t.Fatalf("did not recover corrupt cached credentials: %v", err)
	}
}

// A credential remains usable in memory when secure persistence fails, without appearing in logs.
func TestClientTokenStorageFailureDoesNotLeak(t *testing.T) {
	const secret = "test-device-credential-secret"
	var output bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(oldLog) })
	var asks atomic.Int32
	c := useClientTokens(t, func(w http.ResponseWriter, r *http.Request) {
		asks.Add(1)
		fmt.Fprintf(w, `{"granted_token":{"token":%q,"expires_after_seconds":120}}`, secret)
	})
	c.save = func([]byte) error { return errors.New(secret) }
	for range 2 {
		if got, err := c.get("client"); err != nil || got != secret {
			t.Fatalf("lost credential after storage failure: %v", err)
		}
	}
	if asks.Load() != 1 || strings.Contains(output.String(), secret) {
		t.Fatal("storage failure leaked the credential or caused redundant requests")
	}
}
