package edit

import (
	cryptorand "crypto/rand"
	"time"
)

// Clock is the narrow time dependency used by editor state management.
type Clock interface {
	Now() time.Time
}

// RandomSource is the narrow byte source used for opaque editor tokens.
type RandomSource interface {
	Read([]byte) (int, error)
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

var (
	defaultClock        Clock        = systemClock{}
	defaultRandomSource RandomSource = cryptorand.Reader
)

// Dependencies contains the external inputs used by an editor Server.
type Dependencies struct {
	Clock  Clock
	Random RandomSource
}

func (dependencies Dependencies) withDefaults() Dependencies {
	if dependencies.Clock == nil {
		dependencies.Clock = defaultClock
	}
	if dependencies.Random == nil {
		dependencies.Random = defaultRandomSource
	}
	return dependencies
}
