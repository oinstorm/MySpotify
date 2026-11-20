package player

import (
	"context"
	"errors"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxCertificateAge   = 24 * time.Hour
	maxCertificateBytes = 1 << 20
)

type certificateCache struct {
	mu      sync.Mutex
	loading chan struct{}
	target  string
	reply   drmReply
	expires time.Time
}

func (w *Web) serveCertificate(rw http.ResponseWriter, r *http.Request) {
	gen, _ := strconv.Atoi(r.URL.Query().Get("gen"))
	o := w.out(gen)
	target := licenseHost + "/application-certificate"
	// Keep the cache in Go so every page request stays countable.
	rw.Header().Set("Cache-Control", "no-store")
	c := &w.cert
	for {
		if r.Context().Err() != nil {
			return
		}
		c.mu.Lock()
		if c.target == target && time.Now().Before(c.expires) {
			reply := c.reply
			c.mu.Unlock()
			log.Printf("player: /cert track=%d reason=%s source=cache status=200", gen, drmLoadReason(o))
			reply.write(rw)
			return
		}
		if loading := c.loading; loading != nil {
			c.mu.Unlock()
			select {
			case <-loading:
				continue
			case <-r.Context().Done():
				return
			}
		}
		c.loading = make(chan struct{})
		c.mu.Unlock()

		reply := w.forward(r, o, target)
		c.mu.Lock()
		c.target, c.reply, c.expires = "", drmReply{}, time.Time{}
		if reply.status == http.StatusOK {
			now := time.Now()
			if ttl := certificateLifetime(reply.header, now); ttl > 0 {
				c.target, c.reply, c.expires = target, reply, now.Add(ttl)
			}
		}
		close(c.loading)
		c.loading = nil
		c.mu.Unlock()
		reply.write(rw)
		return
	}
}

func certificateLifetime(h http.Header, now time.Time) time.Duration {
	ttl := maxCertificateAge
	maxAge := false
	for _, directive := range strings.Split(strings.Join(h.Values("Cache-Control"), ","), ",") {
		name, value, _ := strings.Cut(strings.TrimSpace(directive), "=")
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "no-store", "no-cache":
			return 0
		case "max-age":
			seconds, err := strconv.ParseInt(strings.Trim(strings.TrimSpace(value), `"`), 10, 64)
			if maxAge || err != nil || seconds <= 0 {
				return 0
			}
			maxAge = true
			ttl = min(ttl, time.Duration(min(seconds, int64(maxCertificateAge/time.Second)))*time.Second)
		}
	}
	for _, field := range strings.Split(strings.Join(h.Values("Vary"), ","), ",") {
		if field = strings.TrimSpace(field); field != "" && !strings.EqualFold(field, "Accept-Encoding") {
			return 0
		}
	}
	age := time.Duration(0)
	if value := h.Get("Age"); value != "" {
		seconds, err := strconv.ParseInt(value, 10, 64)
		if err != nil || seconds < 0 || seconds >= int64(maxCertificateAge/time.Second) {
			return 0
		}
		age = time.Duration(seconds) * time.Second
	}
	if maxAge {
		if date, err := http.ParseTime(h.Get("Date")); err == nil {
			age = max(age, now.Sub(date))
		}
	} else if value := h.Get("Expires"); value != "" {
		until, err := http.ParseTime(value)
		if err != nil || !now.Before(until) {
			return 0
		}
		return min(ttl-age, until.Sub(now))
	}
	return ttl - age
}

func usableCertificate(body []byte, contentType string) bool {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	return len(body) > 0 && len(body) <= maxCertificateBytes && mediaType != "text/html" &&
		mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json")
}

func certificateTransportError(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "truncated"
	default:
		return "transport-error"
	}
}
