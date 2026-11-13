package transport

import (
	"time"

	"google.golang.org/grpc/backoff"
)

// backoffConfig is how a dropped connection to a peer is retried: quickly at first,
// then no less often than every 15 seconds.
func backoffConfig() backoff.Config {
	return backoff.Config{BaseDelay: time.Second, Multiplier: 1.6, Jitter: 0.2, MaxDelay: 15 * time.Second}
}
