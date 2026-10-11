package blobmirror

import (
	"time"

	"github.com/pyvvo/funcd/internal/platform/clock"
)

// Hooks are the freeze's test seams.
type Hooks struct {
	AfterWalk  func(prefix string)
	AfterLink  func(key string)
	BeforeCopy func()
}

// Set replaces a Mirror's clock, wait, link and hooks; a nil argument keeps the current one.
func Set(m Mirror, c clock.Clock, after func(time.Duration) <-chan time.Time, link func(oldname, newname string) error, h *Hooks) {
	mm := m.(*mirror)
	if c != nil {
		mm.clock = c
	}
	if after != nil {
		mm.after = after
	}
	if link != nil {
		mm.link = link
	}
	if h != nil {
		mm.hooks = hooks{afterWalk: h.AfterWalk, afterLink: h.AfterLink, beforeCopy: h.BeforeCopy}
	}
}

// ObjectID is the object id.
var ObjectID = objectID //nolint:gochecknoglobals // test export
