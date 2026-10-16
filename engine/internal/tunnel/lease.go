package tunnel

import "context"

// Lease is a tunnel lent by its owner (the engine's shared tunnels) for the
// time of one operation.
type Lease struct {
	// Addr is 127.0.0.1:port; Bridge is the instance of the session when lent.
	Addr, Bridge string
	// Release hands the tunnel back; calling it again does nothing.
	Release func()
}

// Lender lends the tunnel of one target.
type Lender interface {
	Lend(ctx context.Context) (Lease, error)
}
