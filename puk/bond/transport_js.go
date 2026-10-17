//go:build js

package bond

import (
	"context"
	"errors"
	"net"
)

func (p Path) Dial(base DialFunc) func(context.Context) (net.Conn, error) {
	return func(context.Context) (net.Conn, error) {
		return nil, errors.New("offline compiler cannot run proxy transports")
	}
}
