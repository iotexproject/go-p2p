package p2p

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core"
	"github.com/stretchr/testify/require"
)

func TestBlockList(t *testing.T) {
	r := require.New(t)

	cfg := DefaultConfig
	now := time.Now()
	name := core.PeerID("alfa")
	withinBlockTTL := now.Add(cfg.BlockListCleanupInterval / 2)
	pastBlockTTL := now.Add(cfg.BlockListCleanupInterval * 2)

	blockTests := []struct {
		curTime time.Time
		blocked bool
	}{
		{now, false},
		{now, false},
		{withinBlockTTL, true},
		{withinBlockTTL, true},
		{pastBlockTTL, false},
		{withinBlockTTL, false},
		{withinBlockTTL, false},
		{pastBlockTTL, false},
		{now, false},
		{now, false},
		{withinBlockTTL, true},
	}

	list := NewCountBlocklist(cfg.BlockListLRUSize, cfg.BlockListErrorThreshold, cfg.BlockListCleanupInterval)
	for _, v := range blockTests {
		list.Add(name, now)
		r.Equal(v.blocked, list.Blocked(name, v.curTime))
	}
	r.Equal(1, list.counter.Len())
	r.Equal(1, list.timeout.Len())

	list.Clear()
	r.Zero(list.counter.Len())
	r.Zero(list.timeout.Len())
}
