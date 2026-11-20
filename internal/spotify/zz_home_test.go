package spotify

import (
	"os"
	"testing"
)

// Guest browsing can load the public home feed without a listener's account.
func TestLiveGuestHome(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("LIVE not set")
	}
	SetIdentity(Identity{})
	SignOut()
	RenewGuest = AnonymousIdentity
	defer func() { RenewGuest = nil; SignOut() }()
	h, err := HomeFeed("")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("greeting %q, %d sections, %d shortcuts", h.Greeting, len(h.Sections), len(h.Shortcuts))
	for i, s := range h.Sections {
		if i > 12 {
			break
		}
		t.Logf("  %q (%d items, recent=%v)", s.Title, len(s.Items), s.Recent)
	}
}
