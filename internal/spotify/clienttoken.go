package spotify

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

var clientTokenURL = "https://clienttoken.spotify.com/v1/clienttoken"

const clientVersion = "1.3.8.509.g26b6eac7300d"

type cachedClientToken struct {
	Token   string    `json:"token"`
	Expires time.Time `json:"expires"`
	Refresh time.Time `json:"refresh"`
}

type clientTokenFile struct {
	Device   string                       `json:"device"`
	Version  string                       `json:"version"`
	Platform string                       `json:"platform"`
	Tokens   map[string]cachedClientToken `json:"tokens"`
}

type clientTokenFailure struct {
	until time.Time
	err   error
}

type clientTokenCache struct {
	mu       sync.Mutex
	device   string
	tokens   map[string]cachedClientToken
	failures map[string]clientTokenFailure
	now      func() time.Time
	save     func([]byte) error
}

func newClientTokenCache() *clientTokenCache {
	return &clientTokenCache{
		tokens:   make(map[string]cachedClientToken),
		failures: make(map[string]clientTokenFailure),
		now:      time.Now,
	}
}

var clientTokens = newClientTokenCache()

// ConfigureClientTokens restores a device's cache before requests start; save must seal credentials before writing them.
func ConfigureClientTokens(device string, data []byte, save func([]byte) error) error {
	if b, err := hex.DecodeString(device); err != nil || len(b) != 16 {
		return errors.New("clienttoken: invalid device ID")
	}
	c := clientTokens
	c.mu.Lock()
	defer c.mu.Unlock()
	c.device, c.save = device, save
	c.tokens = make(map[string]cachedClientToken)
	c.failures = make(map[string]clientTokenFailure)
	if len(data) > 0 {
		var f clientTokenFile
		if json.Unmarshal(data, &f) != nil {
			return errors.New("clienttoken: invalid cached credentials")
		}
		platform, version := clientPlatform()
		if f.Device == device && f.Version == clientVersion && f.Platform == platform+"/"+version {
			for id, token := range f.Tokens {
				if id != "" && token.Token != "" && token.Expires.After(c.now()) && !token.Refresh.IsZero() && !token.Refresh.After(token.Expires) {
					c.tokens[id] = token
				}
			}
		}
	}
	log.Printf("clienttoken: stable device configured, %d cached credentials restored", len(c.tokens))
	return nil
}

func clientToken(clientID string) (string, error) { return clientTokens.get(clientID) }

func (c *clientTokenCache) get(clientID string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if clientID == "" {
		return "", errors.New("clienttoken: no client ID")
	}
	now := c.now()
	kept := c.tokens[clientID]
	valid := kept.Token != "" && now.Before(kept.Expires)
	if valid && now.Before(kept.Refresh) {
		log.Print("clienttoken: reusing cached device credential")
		return kept.Token, nil
	}
	if failure := c.failures[clientID]; now.Before(failure.until) {
		if valid {
			return kept.Token, nil
		}
		return "", failure.err
	}
	if c.device == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", fmt.Errorf("clienttoken: device identity: %w", err)
		}
		c.device = hex.EncodeToString(b[:])
	}
	token, err := requestClientToken(clientID, c.device, now)
	if err == nil && !token.Expires.IsZero() && !token.Expires.After(c.now()) {
		err = errors.New("clienttoken: credential expired before the request completed")
	}
	if err != nil {
		delay := time.Minute
		var throttled *Throttled
		if errors.As(err, &throttled) {
			delay = max(delay, throttled.Wait)
		}
		c.failures[clientID] = clientTokenFailure{c.now().Add(delay), err}
		if valid && c.now().Before(kept.Expires) {
			log.Print("clienttoken: refresh deferred, cached credential still valid")
			return kept.Token, nil
		}
		return "", err
	}
	delete(c.failures, clientID)
	if token.Expires.After(c.now()) {
		c.tokens[clientID] = token
	} else {
		delete(c.tokens, clientID)
	}
	if c.save != nil {
		platform, version := clientPlatform()
		data, err := json.Marshal(clientTokenFile{c.device, clientVersion, platform + "/" + version, c.tokens})
		if err == nil {
			err = c.save(data)
		}
		if err != nil {
			log.Print("clienttoken: device credential could not be kept")
		}
	}
	log.Print("clienttoken: device credential refreshed")
	return token.Token, nil
}

func requestClientToken(clientID, device string, now time.Time) (cachedClientToken, error) {
	platform, version := clientPlatform()
	payload := map[string]any{"client_data": map[string]any{
		"client_version": clientVersion,
		"client_id":      clientID,
		"js_sdk_data": map[string]any{
			"device_brand": "unknown", "device_model": "unknown",
			"os": platform, "os_version": version, "container_version": "unknown",
			"device_id": device, "device_type": "computer", "platform_identifier": "web_player",
		},
	}}
	body, err := json.Marshal(payload)
	if err != nil {
		return cachedClientToken{}, err
	}
	req, err := http.NewRequest(http.MethodPost, clientTokenURL, bytes.NewReader(body))
	if err != nil {
		return cachedClientToken{}, err
	}
	req.Header.Set("User-Agent", tokenUserAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := Client.Do(req)
	if err != nil {
		return cachedClientToken{}, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusTooManyRequests {
		return cachedClientToken{}, &Throttled{Wait: RetryAfter(res.Header)}
	}
	if res.StatusCode != http.StatusOK {
		return cachedClientToken{}, fmt.Errorf("clienttoken: %s", res.Status)
	}
	var reply struct {
		Granted struct {
			Token   string `json:"token"`
			Expires *int64 `json:"expires_after_seconds"`
			Refresh *int64 `json:"refresh_after_seconds"`
		} `json:"granted_token"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&reply) != nil {
		return cachedClientToken{}, errors.New("clienttoken: invalid response")
	}
	v := reply.Granted
	if v.Token == "" {
		return cachedClientToken{}, errors.New("clienttoken: none granted")
	}
	token := cachedClientToken{Token: v.Token}
	if v.Expires != nil {
		if *v.Expires <= 0 || *v.Expires > int64((1<<63-1)/time.Second) {
			return cachedClientToken{}, errors.New("clienttoken: invalid credential expiry")
		}
		token.Expires = now.Add(time.Duration(*v.Expires) * time.Second)
		refresh := *v.Expires
		if v.Refresh != nil && *v.Refresh >= 0 && *v.Refresh < refresh {
			refresh = *v.Refresh
		}
		token.Refresh = now.Add(time.Duration(refresh) * time.Second)
	}
	return token, nil
}
