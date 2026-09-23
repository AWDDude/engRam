package daemon

import (
	"fmt"

	"github.com/gofrs/flock"
)

// takeLock tries to take the daemon's exclusive lock without blocking.
//
// The daemon holds this for its whole life, so it is the single-owner
// guarantee: whatever else races, only one process gets past it and opens the
// bolt file. The bool reports whether the lock was acquired; false with a nil
// error means someone else holds it, which is an outcome rather than a fault.
func takeLock(path string) (*flock.Flock, bool, error) {
	lock := flock.New(path)
	held, err := lock.TryLock()
	if err != nil {
		return nil, false, fmt.Errorf("taking daemon lock %s: %w", path, err)
	}
	return lock, held, nil
}
