package p2p

import (
	"context"

	"github.com/libp2p/go-libp2p/core"
)

type broadcastCtxKey struct{}

type unicastCtxKey struct{}

// GetUnicastStream retrieves net.Stream from unicast request context.
func GetUnicastStream(ctx context.Context) (core.Stream, bool) {
	s, ok := ctx.Value(unicastCtxKey{}).(core.Stream)
	return s, ok
}
