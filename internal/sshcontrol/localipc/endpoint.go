package localipc

import "errors"

// ErrEndpointTooLong means the user's state path cannot fit in a native Unix
// socket address. The controller must refuse startup before leaving a lock.
var ErrEndpointTooLong = errors.New("ssh local ipc: endpoint path too long")
