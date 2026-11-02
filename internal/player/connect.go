package player

import (
	"time"
	"vinyl/internal/spotify"
)

type QueueRequest struct {
	Tracks []spotify.Track
	Start  int
}

func (p *Player) ConnectQueue(tracks []spotify.Track) {
	if len(tracks) == 0 || p.track == nil || p.track.URI() != tracks[0].URI() {
		return
	}
	renamed := p.track.ArtistNames() != tracks[0].ArtistNames()
	current := p.track.URI()
	p.queue = append([]spotify.Track(nil), tracks...)
	p.order = make([]int, len(tracks))
	for index := range p.order {
		p.order[index] = index
	}
	if p.shuffle {
		first := 0
		for index, track := range p.queue {
			if track.URI() == current {
				first = index
				break
			}
		}
		p.shuffleOrder(first)
	}
	p.at = 0
	p.track = &p.queue[0]
	if renamed {
		// Its artist named only now: the system, and what was played, hear.
		p.announce(p.track, p.cover)
		p.sync()
	}
	// What comes next may be known only now.
	p.lyricsAhead()
}

// ConnectOptions takes the shuffle and repeat a remote reported. Shuffle and
// repeat play one at a time: when Spotify reports both, the one it changed is
// what the listener asked for, and the other is dropped.
func (p *Player) ConnectOptions(options spotify.ConnectOptions) {
	shuffle, repeat := p.shuffle, p.repeat
	if options.Shuffle != nil {
		want := *options.Shuffle
		if p.shufflePending && want == p.shuffle {
			p.shufflePending = false
			p.shuffleGen++
		} else if !p.shufflePending {
			shuffle = want
		}
	}
	if options.RepeatContext != nil && options.RepeatTrack != nil {
		mode := RepeatOff
		if *options.RepeatContext {
			mode = RepeatAll
		}
		if *options.RepeatTrack {
			mode = RepeatOne
		}
		if p.repeatPending && mode == p.repeat {
			p.repeatPending = false
			p.repeatGen++
		} else if !p.repeatPending {
			repeat = mode
		}
	}
	if shuffle && repeat != RepeatOff {
		// The other of the two went on as this one was reported: it stays.
		if repeat != p.repeat && shuffle == p.shuffle {
			shuffle = false
		} else {
			repeat = RepeatOff
		}
	}
	p.shuffle, p.repeat = shuffle, repeat
	p.sync()
}
func (p *Player) control(action string, value any) bool {
	return p.Control != nil && p.Control(action, value)
}

func (p *Player) ApplyConnect(track spotify.Track, position time.Duration, paused bool) {
	control := p.Control
	p.Control = nil
	defer func() { p.Control = control }()
	_, remote := p.out.(*remoteOutput)
	if p.track != nil && p.track.URI() == track.URI() && !remote && (p.out != nil || p.loading) {
		if position >= 0 {
			p.Seek(position)
		}
		if p.playing == paused {
			p.TogglePlay()
		}
		return
	}
	p.endRadio()
	p.queue = []spotify.Track{track}
	p.order = []int{0}
	p.loadSkipping(0, 0, !paused, max(0, position))
}

func (p *Player) MirrorConnect(track spotify.Track, position time.Duration, playing bool) {
	output, remote := p.out.(*remoteOutput)
	if !remote || p.track == nil || p.track.URI() != track.URI() {
		p.endRadio()
		p.queue = []spotify.Track{track}
		p.order = []int{0}
		p.cue(0, false, 0)
		output = &remoteOutput{duration: track.Length()}
		p.out = output
	}
	output.position, output.at, output.playing = position, time.Now(), playing
	p.playing, p.loading = playing, false
	p.sync()
}

func (p *Player) StopConnect() { p.playing = false; p.stop(); p.sync() }

func (p *Player) IsRemote() bool { _, ok := p.out.(*remoteOutput); return ok }

type remoteOutput struct {
	position, duration time.Duration
	at                 time.Time
	playing            bool
}

func (o *remoteOutput) Play()                   {}
func (o *remoteOutput) Pause()                  {}
func (o *remoteOutput) Seek(time.Duration)      {}
func (o *remoteOutput) SetVolume(float64)       {}
func (o *remoteOutput) Close()                  {}
func (o *remoteOutput) Playing() bool           { return o.playing }
func (o *remoteOutput) Duration() time.Duration { return o.duration }
func (o *remoteOutput) OnEnd(func()) func()     { return func() {} }
func (o *remoteOutput) Position() time.Duration {
	position := o.position
	if o.playing {
		position += time.Since(o.at)
	}
	return min(max(0, position), o.duration)
}
