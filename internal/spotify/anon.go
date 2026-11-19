package spotify

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// AnonymousIdentity gets an identity from Spotify without anyone signing
// in: the web player asks /api/token for one, proving with a one-time code
// that it is the web player. It grants what an anonymous visitor sees —
// search, browsing, lyrics — but neither playback nor anything of a
// listener's own.
//
// The client token lasts a fortnight. A failure to get one is not fatal
// here: browsing goes through Pathfinder, which does not want one.
func AnonymousIdentity() (Identity, error) {
	r, err := apiToken("")
	if err != nil {
		return Identity{}, fmt.Errorf("spotify: anonymous token: %w", err)
	}
	id := r.id
	id.clientID = r.clientID
	if ct, err := clientToken(r.clientID); err == nil {
		id.ClientToken = ct
	}
	return id, nil
}

// serverTimeOf reads Spotify's clock.
func serverTimeOf() (time.Time, error) {
	req, err := http.NewRequest(http.MethodGet, serverTimeURL, nil)
	if err != nil {
		return time.Time{}, err
	}
	req.Header.Set("User-Agent", UserAgent)
	res, err := Client.Do(req)
	if err != nil {
		return time.Time{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("server-time: %s", res.Status)
	}
	var v struct {
		ServerTime int64 `json:"serverTime"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&v); err != nil {
		return time.Time{}, err
	}
	if v.ServerTime <= 0 {
		return time.Time{}, fmt.Errorf("server-time: none in the reply")
	}
	return time.Unix(v.ServerTime, 0), nil
}
