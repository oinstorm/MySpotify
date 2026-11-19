package player

import "time"

// licenceGate keeps track of Spotify turning the device away for asking too
// often (429 at the licence service), so that one refusal is not answered by
// more asking: asking again at once keeps a run of refusals going, as
// librespot found out with the audio keys (librespot-org/librespot#1649).
type licenceGate struct {
	until    time.Time // no licence is asked before
	fails    int       // refusals in a row, which lengthen the wait
	last     time.Time // last refusal or newly authorized track that played through
	good     int       // consecutive newly authorized tracks that played through
	lastGood int       // player generation last counted, so repeat-one counts once
}

// gateQuiet is how long without a refusal the wait goes back to its first
// step.
const gateQuiet = 10 * time.Minute

const (
	gateGood = 3
	gateStop = 4
)

// refused notes a refusal, and returns until when no licence is to be asked:
// as long as Spotify said, or longer each time it did not say.
func (g *licenceGate) refused(now time.Time, wait time.Duration) time.Time {
	if !g.last.IsZero() && now.Sub(g.last) >= gateQuiet {
		g.fails = 0
	}
	if wait <= 0 {
		wait = 30 * time.Second << min(g.fails, 4)
	}
	g.fails++
	g.last = now
	g.good = 0
	if until := now.Add(wait); until.After(g.until) {
		g.until = until
	}
	return g.until
}

func (g *licenceGate) manual() bool { return g.fails >= gateStop }

// shut reports whether no licence may be asked at now.
func (g *licenceGate) shut(now time.Time) bool { return now.Before(g.until) }

// left is how long the wait still has at now.
func (g *licenceGate) left(now time.Time) time.Duration {
	if !g.shut(now) {
		return 0
	}
	return g.until.Sub(now)
}

// open lets licences be asked for again: the wait is over. What was refused
// before is still remembered, to wait longer if it happens again.
func (g *licenceGate) open() { g.until = time.Time{} }

// completed counts a newly authorized output once, not on every loop.
func (g *licenceGate) completed(now time.Time, gen int) {
	if g.fails == 0 || gen == g.lastGood {
		return
	}
	g.last, g.lastGood = now, gen
	g.good++
	if g.good == gateGood {
		g.fails--
		g.good = 0
	}
}
