package fleet

import "errors"

// Fleet coordination errors
var (
	// ErrPeerFetchFailed indicates a peer failed to fetch a package
	ErrPeerFetchFailed = errors.New("peer failed to fetch package")

	// ErrTimeout indicates a coordination timeout
	ErrTimeout = errors.New("fleet coordination timeout")

	// ErrNoFleetPeers indicates no mDNS peers are available
	ErrNoFleetPeers = errors.New("no fleet peers available")

	// ErrCoordinatorClosed indicates the coordinator has been shut down
	ErrCoordinatorClosed = errors.New("coordinator closed")
)
