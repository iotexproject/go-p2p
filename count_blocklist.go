package p2p

import (
	"time"

	"github.com/iotexproject/go-pkgs/cache"
	"github.com/libp2p/go-libp2p-core"
)

// CountBlocklist is a count-based blacklist implementation. Entry is being added
// to an LRU cache, once the count reaches a threshold the entry is blocked for a
// certain timeout. The entry is then unblocked once the timeout expires
type CountBlocklist struct {
	count   int           // number of times an entry is added before being blocked
	ttl     time.Duration // timeout an entry will be blocked
	counter *cache.ThreadSafeLruCache
	timeout *cache.ThreadSafeLruCache
}

// NewCountBlocklist creates a new CountBlocklist
func NewCountBlocklist(cap, count int, ttl time.Duration) *CountBlocklist {
	return &CountBlocklist{
		count:   count,
		ttl:     ttl,
		counter: cache.NewThreadSafeLruCache(cap),
		timeout: cache.NewThreadSafeLruCache(cap),
	}
}

// Blocked returns true if the name is blocked
func (b *CountBlocklist) Blocked(name core.PeerID, t time.Time) bool {
	v, ok := b.timeout.Get(name)
	if !ok {
		return false
	}

	if v.(time.Time).After(t) {
		return true
	}

	// timeout passed, remove name off the blocklist
	b.remove(name)
	return false
}

// Add tries to add the name to blocklist
func (b *CountBlocklist) Add(name core.PeerID, t time.Time) {
	var count int
	if v, ok := b.counter.Get(name); !ok {
		count = 1
	} else {
		count = v.(int) + 1
	}
	b.counter.Add(name, count)

	// add to blocklist once reaching count
	if count >= b.count {
		b.timeout.Add(name, t.Add(b.ttl))
	}
}

// Clear clears the blocklist
func (b *CountBlocklist) Clear() {
	b.timeout.Clear()
	b.counter.Clear()
}

// remove takes name off the blocklist
func (b *CountBlocklist) remove(name core.PeerID) {
	b.counter.Remove(name)
	b.timeout.Remove(name)
}
