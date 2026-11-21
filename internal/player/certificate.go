package player

import (
	"context"
	"errors"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"slices"
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
	origin  []string
	reply   drmReply
	expires time.Time
}

func (w *Web) serveCertificate(rw http.ResponseWriter, r *http.Request, origin []string) {
	origin = slices.Clone(origin)
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
		now := time.Now()
		if c.target == target && slices.Equal(c.origin, origin) && now.Before(c.expires) {
			reply := c.reply
			ttl := time.Until(c.expires).Round(time.Second)
			c.mu.Unlock()
			log.Printf("player: /cert page=%d track=%d reason=%s source=cache status=200 ttl=%s", w.id, gen, drmLoadReason(o), ttl)
			reply.write(rw)
			return
		}
		miss := "empty"
		switch {
		case c.target != "" && c.target != target:
			miss = "target-changed"
		case c.target == target && !slices.Equal(c.origin, origin):
			miss = "origin-changed"
		case c.target == target && !c.expires.IsZero() && !now.Before(c.expires):
			miss = "expired"
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

		reply := w.forward(r, o, target, origin)
		c.mu.Lock()
		c.target, c.origin, c.reply, c.expires = "", nil, drmReply{}, time.Time{}
		cacheDecision := reply.cacheReason
		if reply.status == http.StatusOK && reply.upstreamStatus == http.StatusOK {
			now := time.Now()
			ttl, policy := certificateLifetime(reply.header, now)
			cacheDecision = policy
			if ttl > 0 {
				c.target, c.origin, c.reply, c.expires = target, origin, reply, now.Add(ttl)
				cacheDecision = "stored-" + policy
			}
		} else if cacheDecision == "" {
			cacheDecision = "status-" + strconv.Itoa(reply.upstreamStatus)
		}
		ttl := time.Duration(0)
		if c.target == target {
			ttl = time.Until(c.expires).Round(time.Second)
		}
		close(c.loading)
		c.loading = nil
		c.mu.Unlock()
		log.Printf("player: /cert page=%d track=%d reason=%s source=upstream attempt=%d status=%d upstream_status=%d miss=%s cache=%s ttl=%s cache_control=%q vary=%q age=%q expires=%q", w.id, gen, drmLoadReason(o), reply.attempt, reply.status, reply.upstreamStatus, miss, cacheDecision, ttl, certificatePolicyHeader(reply.header, "Cache-Control"), certificatePolicyHeader(reply.header, "Vary"), certificatePolicyHeader(reply.header, "Age"), certificatePolicyHeader(reply.header, "Expires"))
		reply.write(rw)
		return
	}
}

func certificateLifetime(h http.Header, now time.Time) (time.Duration, string) {
	ttl := maxCertificateAge
	maxAge := false
	for _, directive := range strings.Split(strings.Join(h.Values("Cache-Control"), ","), ",") {
		name, value, _ := strings.Cut(strings.TrimSpace(directive), "=")
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "no-store", "no-cache":
			return 0, strings.ToLower(strings.TrimSpace(name))
		case "max-age":
			seconds, err := strconv.ParseInt(strings.Trim(strings.TrimSpace(value), `"`), 10, 64)
			if maxAge || err != nil || seconds <= 0 {
				if err != nil {
					return 0, "invalid-max-age"
				}
				return 0, "duplicate-or-zero-max-age"
			}
			maxAge = true
			ttl = min(ttl, time.Duration(min(seconds, int64(maxCertificateAge/time.Second)))*time.Second)
		}
	}
	for _, field := range strings.Split(strings.Join(h.Values("Vary"), ","), ",") {
		if field = strings.TrimSpace(field); field != "" && !strings.EqualFold(field, "Accept-Encoding") && !strings.EqualFold(field, "Origin") {
			return 0, "vary-" + strings.ToLower(field)
		}
	}
	age := time.Duration(0)
	if value := h.Get("Age"); value != "" {
		seconds, err := strconv.ParseInt(value, 10, 64)
		if err != nil || seconds < 0 || seconds >= int64(maxCertificateAge/time.Second) {
			return 0, "invalid-or-expired-age"
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
			return 0, "invalid-or-expired-expires"
		}
		ttl = min(ttl-age, until.Sub(now))
		if ttl <= 0 {
			return 0, "expired-expires"
		}
		return ttl, "expires"
	}
	ttl -= age
	if ttl <= 0 {
		return 0, "expired-max-age"
	}
	if maxAge {
		return ttl, "max-age"
	}
	return ttl, "default-24h"
}

func certificatePolicyHeader(h http.Header, name string) string {
	v := strings.Join(h.Values(name), ",")
	if len(v) > 160 {
		return v[:160]
	}
	return v
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
