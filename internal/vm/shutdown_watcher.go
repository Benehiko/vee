package vm

import "time"

// shutdownWatcherDialTimeout bounds how long the QMP owner will wait for the
// QMP socket to come up before giving up. The Start path already waited for the
// same socket, so this only covers the goroutine startup race.
const shutdownWatcherDialTimeout = 5 * time.Second
