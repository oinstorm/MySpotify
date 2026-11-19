package player

import "time"

// licenceGate keeps track of Spotify turning the device away for asking too
// often (429 at the licence service), so that one refusal is not answered by
// more asking: asking again at once keeps a run of refusals going, as
// librespot found out with the audio keys (librespot-org/librespot#1649).
//
// While it is shut no licence is asked. Each refusal waits longer, and only a
// quiet spell, or a track played long enough, brings the wait back to its
// first step: one lucky track no longer hides that the device is limited.
type licenceGate struct {
	until  time.Time // no licence is asked before
	fails  int       // refusals in a row, which lengthen the wait
	last   time.Time // when it was last refused
	played time.Time // when a track started playing, while refused before
}

// gateQuiet is how long without a refusal the wait goes back to its first
// step.
const gateQuiet = 10 * time.Minute

// gateGood is how long a track plays, after a refusal, before what is left
// of the wait is let go: a track that only starts is not enough.
const gateGood = 30 * time.Second

// refused notes a refusal, and returns until when no licence is to be asked:
// as long as Spotify said, or longer each time it did not say.
func (g *licenceGate) refused(now time.Time, wait time.Duration) time.Time {
	if !g.last.IsZero() && now.Sub(g.last) >= gateQuiet {
		g.fails = 0
	}
	if wait <= 0 {
		wait = 30 * time.Second << min(g.fails, 4)
	}
	wait = min(wait, 30*time.Minute)
	g.fails++
	g.last = now
	if until := now.Add(wait); until.After(g.until) {
		g.until = until
	}
	g.played = time.Time{}
	return g.until
}

// shut reports whether no licence may be asked at now.
func (g *licenceGate) shut(now time.Time) bool { return now.Before(g.until) }

// left is how long the wait still has at now.
func (g *licenceGate) left(now time.Time) time.Duration {
	if !g.shut(now) {
		return 0
	}
	return g.until.Sub(now)
}

// started notes that a track began playing at now, that a refusal before it
// may be forgotten once it has played gateGood.
func (g *licenceGate) started(now time.Time) {
	if g.fails > 0 {
		g.played = now
	}
}

// open lets licences be asked for again: the wait is over. What was refused
// before is still remembered, to wait longer if it happens again.
func (g *licenceGate) open() { g.until = time.Time{} }

// playedFor forgets the refusals in a row once a track has played long
// enough: a licence granted and playing for half a minute says the run is
// over, where one that only started says nothing.
func (g *licenceGate) playedFor(now time.Time) {
	if g.played.IsZero() || now.Sub(g.played) < gateGood {
		return
	}
	g.played, g.fails = time.Time{}, 0
}
