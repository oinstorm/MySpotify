package player

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func certificateRequest(w *Web, gen int) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "http://"+w.addr+"/cert?gen="+strconv.Itoa(gen), nil)
	reply := httptest.NewRecorder()
	w.ServeHTTP(reply, r)
	return reply
}

// Switching tracks reuses the application certificate, but each licence still goes upstream.
func TestCertificateReusedAcrossTracks(t *testing.T) {
	var certs, licences atomic.Int32
	ch, p, _ := licensed(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/application-certificate":
			certs.Add(1)
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write([]byte{0x30, 0x02, 0x00, 0x01})
		case r.Method == http.MethodPost:
			licences.Add(1)
			w.Write([]byte("licence"))
		default:
			http.NotFound(w, r)
		}
	})
	for gen := 1; gen <= 2; gen++ {
		r := certificateRequest(p.Web, gen)
		if r.Code != http.StatusOK || !bytes.Equal(r.Body.Bytes(), []byte{0x30, 0x02, 0x00, 0x01}) || r.Header().Get("Content-Type") != "application/octet-stream" {
			t.Fatalf("track %d did not receive the complete certificate: %d", gen, r.Code)
		}
		if got := page(p, http.MethodPost, fmt.Sprintf("/license?gen=%d", gen), "challenge"); got != http.StatusOK {
			t.Fatalf("licence for track %d got %d", gen, got)
		}
		if gen == 1 {
			p.Next()
			next(t, ch)
		}
	}
	if certs.Load() != 1 || licences.Load() != 2 {
		t.Fatalf("upstream got %d certificate and %d licence requests, want 1 and 2", certs.Load(), licences.Load())
	}
	otherPage := &Web{addr: p.Web.addr}
	if r := certificateRequest(otherPage, 1); r.Code != http.StatusOK || certs.Load() != 2 {
		t.Fatalf("a separate playback page must get its own certificate: status %d, requests %d", r.Code, certs.Load())
	}
}

// Once the application's certificate expires, the next track uses the newly fetched certificate.
func TestCertificateRefetchedOnExpiry(t *testing.T) {
	var asks atomic.Int32
	_, p, _ := licensed(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, asks.Add(1))
	})
	if r := certificateRequest(p.Web, 1); r.Code != http.StatusOK || r.Body.String() != "1" {
		t.Fatalf("first request got %d: %q", r.Code, r.Body.String())
	}
	p.Web.cert.mu.Lock()
	p.Web.cert.expires = time.Unix(1, 0)
	p.Web.cert.mu.Unlock()
	if r := certificateRequest(p.Web, 1); r.Code != http.StatusOK || r.Body.String() != "2" || asks.Load() != 2 {
		t.Fatalf("certificate was not refreshed: status %d, requests %d", r.Code, asks.Load())
	}
}

// A certificate which Spotify marks no-store must go upstream even on the very next track.
func TestCertificateNoStoreBypassesCache(t *testing.T) {
	var asks atomic.Int32
	_, p, _ := licensed(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, asks.Add(1))
	})
	for n := 1; n <= 2; n++ {
		if r := certificateRequest(p.Web, 1); r.Code != http.StatusOK || r.Body.String() != strconv.Itoa(n) {
			t.Fatalf("request %d got %d: %q", n, r.Code, r.Body.String())
		}
	}
}

// Rejected or unusable certificate responses never become a cache hit on the next track.
func TestCertificateDoesNotCacheFailures(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		contentType string
		body        string
		truncated   bool
		want        int
	}{
		{name: "rate-limited", status: 429, want: 429},
		{name: "bad-gateway", status: 502, want: 502},
		{name: "empty", status: 200, want: 502},
		{name: "json", status: 200, contentType: "application/json; charset=utf-8", body: `{}`, want: 502},
		{name: "html", status: 200, contentType: "text/html", body: "<html>", want: 502},
		{name: "oversize", status: 200, body: strings.Repeat("x", maxCertificateBytes+1), want: 502},
		{name: "truncated", status: 200, body: "x", truncated: true, want: 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var asks atomic.Int32
			_, p, _ := licensed(t, func(w http.ResponseWriter, r *http.Request) {
				if asks.Add(1) != 1 {
					w.Write([]byte("certificate"))
					return
				}
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				if tc.status == http.StatusTooManyRequests {
					w.Header().Set("Retry-After", "15")
				}
				if tc.truncated {
					w.Header().Set("Content-Length", "100")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			r := certificateRequest(p.Web, 1)
			if r.Code != tc.want {
				t.Fatalf("bad certificate got %d, want %d", r.Code, tc.want)
			}
			if tc.status == http.StatusTooManyRequests && r.Header().Get("Retry-After") != "15" {
				t.Fatal("did not forward the certificate's retry deadline")
			}
			if r := certificateRequest(p.Web, 1); r.Code != http.StatusOK || r.Body.String() != "certificate" || asks.Load() != 2 {
				t.Fatalf("bad certificate was not replaced: status %d, requests %d", r.Code, asks.Load())
			}
		})
	}
}

// A cacheable certificate has one upstream fetch even when several tracks ask before it arrives.
func TestCertificateSharesConcurrentFetch(t *testing.T) {
	var asks atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	_, p, _ := licensed(t, func(w http.ResponseWriter, r *http.Request) {
		if asks.Add(1) == 1 {
			close(started)
			<-release
		}
		w.Write([]byte("certificate"))
	})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if r := certificateRequest(p.Web, 1); r.Code != http.StatusOK || r.Body.String() != "certificate" {
				t.Errorf("did not receive the shared certificate: %d", r.Code)
			}
		})
	}
	<-started
	close(release)
	wg.Wait()
	if asks.Load() != 1 {
		t.Fatalf("got %d certificate requests upstream, want 1", asks.Load())
	}
}

// Certificate reuse never extends the upstream freshness deadline or reuses a varied response.
func TestCertificateHonorsFreshnessHeaders(t *testing.T) {
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		control, vary, age, expires string
		want                        time.Duration
	}{
		{want: 24 * time.Hour},
		{control: "private, max-age=60", age: "10", want: 50 * time.Second},
		{control: "max-age=60", age: "61", want: -time.Second},
		{control: "max-age=1000000", want: 24 * time.Hour},
		{control: "max-age=0"},
		{control: "max-age=60, no-store"},
		{control: "no-cache"},
		{vary: "client-token"},
		{vary: "Accept-Encoding", want: 24 * time.Hour},
		{expires: now.Add(time.Minute).Format(http.TimeFormat), want: time.Minute},
		{expires: now.Add(-time.Minute).Format(http.TimeFormat)},
	} {
		h := make(http.Header)
		h.Set("Cache-Control", tc.control)
		h.Set("Vary", tc.vary)
		h.Set("Age", tc.age)
		h.Set("Expires", tc.expires)
		if got := certificateLifetime(h, now); got != tc.want {
			t.Errorf("headers %v gave %v, want %v", h, got, tc.want)
		}
	}
}
