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
	withinBlockTTL := now.Add(cfg.BlackListCleanupInterval / 2)
	pastBlockTTL := now.Add(cfg.BlackListCleanupInterval * 2)

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
	}

	list := NewCountBlocklist(cfg.BlackListLRUSize, cfg.BlackListErrorThreshold, cfg.BlackListCleanupInterval)
	for _, v := range blockTests {
		list.Add(name, now)
		r.Equal(v.blocked, list.Blocked(name, v.curTime))
	}
}
