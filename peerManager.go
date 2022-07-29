package p2p

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"

	"github.com/iotexproject/go-pkgs/cache/ttl"
	core "github.com/libp2p/go-libp2p-core"
	"github.com/libp2p/go-libp2p-core/peer"
	discovery "github.com/libp2p/go-libp2p-discovery"
	"github.com/multiformats/go-multiaddr"
	"go.uber.org/zap"
)

var (
	errNoDiscoverer = errors.New("discoverer isn't set")
	errNoPeers      = errors.New("no peers are found")
)

const _connectRetry = 5

type peerManagerOpt func(cfg *peerManager)

func withMaxPeers(num int) peerManagerOpt {
	return func(pm *peerManager) {
		pm.maxPeers = num
	}
}

func withBlacklistTolerance(num int) peerManagerOpt {
	return func(pm *peerManager) {
		pm.blacklistTolerance = num
	}
}

func withBlacklistTimeout(t time.Duration) peerManagerOpt {
	return func(pm *peerManager) {
		pm.blacklistTimeout = t
	}
}

func withAdvertiseInterval(t time.Duration) peerManagerOpt {
	return func(pm *peerManager) {
		pm.blacklistTimeout = t
	}
}

type peerManager struct {
	routing  *discovery.RoutingDiscovery
	host     core.Host
	ns       string
	maxPeers int // unlimited peers when maxPeers = 0

	bootstrap      map[core.PeerID]bool
	blacklist      *ttl.Cache
	advertiseQueue chan string
	findPeersQueue chan int

	blacklistTolerance int
	blacklistTimeout   time.Duration
	advertiseInterval  time.Duration

	once sync.Once
}

func newPeerManager(host core.Host, routing *discovery.RoutingDiscovery, ns string, opts ...peerManagerOpt) *peerManager {
	rand.Seed(time.Now().UnixNano())
	pm := &peerManager{
		host:              host,
		routing:           routing,
		ns:                ns,
		bootstrap:         make(map[peer.ID]bool),
		advertiseInterval: 3 * time.Hour, // DHT provider record is republished every 3hrs by default
	}
	for _, opt := range opts {
		opt(pm)
	}
	if pm.blacklistTimeout > 0 {
		bl, _ := ttl.NewCache(ttl.AutoExpireOption(pm.blacklistTimeout))
		pm.blacklist = bl
	} else {
		bl, _ := ttl.NewCache()
		pm.blacklist = bl
	}
	return pm
}

func (pm *peerManager) JoinOverlay() {
	pm.once.Do(func() {
		pm.advertiseQueue = make(chan string, 1)
		ticker := time.NewTicker(pm.advertiseInterval)
		go func(pm *peerManager) {
			for {
				select {
				case ns := <-pm.advertiseQueue:
					_, err := pm.routing.Advertise(context.Background(), ns)
					if err != nil {
						Logger().Error("error when advertising.", zap.Error(err))
					}
				case <-ticker.C:
					_, err := pm.routing.Advertise(context.Background(), pm.ns)
					if err != nil {
						Logger().Error("error when advertising.", zap.Error(err))
					}
				}
			}
		}(pm)

		pm.findPeersQueue = make(chan int, 1)
		go func(pm *peerManager) {
			for limit := range pm.findPeersQueue {
				if err := pm.connectPeers(context.Background(), limit); err != nil {
					Logger().Error("error when finding peers", zap.Error(err))
				}
			}
		}(pm)
	})
}

func (pm *peerManager) Advertise() error {
	if pm.advertiseQueue == nil {
		return errors.New("the host doesn't join the overlay")
	}
	pm.advertiseQueue <- pm.ns
	return nil
}

func (pm *peerManager) AddBootstrap(ids ...core.PeerID) {
	for _, id := range ids {
		pm.bootstrap[id] = true
	}
}

func (pm *peerManager) ConnectPeers() {
	select {
	case pm.findPeersQueue <- pm.peerLimit():
	default:
	}
}

func (pm *peerManager) connectPeers(ctx context.Context, limit int) error {
	// skip connecting when 80% peers are connected
	if len(pm.host.Network().Conns()) >= pm.threshold() {
		return nil
	}
	if pm.routing == nil {
		panic(errNoDiscoverer)
	}
	for retries := 0; retries < _connectRetry; retries++ {
		reachLimit, err := pm.findPeers(ctx, limit)
		if err != nil {
			return err
		}
		// directly return when peers are found
		if pm.hasPeers() {
			return nil
		}
		// err when no more peers can be found and discovery limit isn't reached
		if !reachLimit {
			break
		}
		// try to find peers by increasing peerlimit in the next round
		limit += limit
	}
	return errNoPeers
}

func (pm *peerManager) findPeers(ctx context.Context, limit int) (bool, error) {
	peerChan, err := pm.routing.FindPeers(ctx, pm.ns, discovery.Limit(limit))
	if err != nil {
		return false, err
	}
	cnt := 0
	for peer := range peerChan {
		cnt++
		if peer.ID == pm.host.ID() {
			continue
		}
		pm.host.Connect(ctx, peer)
	}
	return cnt == limit, nil
}

func (pm *peerManager) hasPeers() bool {
	for _, conn := range pm.host.Network().Conns() {
		if !pm.bootstrap[conn.RemotePeer()] {
			return true
		}
	}
	return false
}

func (pm *peerManager) DisconnectPeer(pr peer.ID) error {
	return pm.host.Network().ClosePeer(pr)
}

func (pm *peerManager) TryBlockPeer(pr peer.ID) {
	val, exist := pm.blacklist.Get(pr)
	if !exist {
		pm.blacklist.Set(pr, 1)
		return
	}
	pm.blacklist.Set(pr, val.(int)+1)
	if val.(int)+1 >= pm.blacklistTolerance {
		pm.host.Network().ClosePeer(pr)
	}
}

func (pm *peerManager) BlockPeer(pr peer.ID) {
	pm.blacklist.Set(pr, pm.blacklistTolerance)
	pm.host.Network().ClosePeer(pr)
}

func (pm *peerManager) ClearBlockList() {
	pm.blacklist.Reset()
}

func (pm *peerManager) Blocked(pr peer.ID) bool {
	return pm.blocked(pr)
}

func (pm *peerManager) blocked(pr peer.ID) bool {
	val, exist := pm.blacklist.Get(pr)
	if !exist {
		return false
	}
	return val.(int) >= pm.blacklistTolerance
}

func (pm *peerManager) ConnectedPeers() []peer.AddrInfo {
	ret := make([]core.PeerAddrInfo, 0)
	conns := pm.host.Network().Conns()
	// There might be multiple connections for one peerID,
	// but only a random one is added into the result
	connSet := make(map[string]bool, len(conns))
	for _, conn := range conns {
		remoteID := conn.RemotePeer()
		if connSet[remoteID.Pretty()] {
			continue
		}
		if pm.blocked(remoteID) {
			continue
		}
		if pm.bootstrap[remoteID] {
			continue
		}
		ret = append(ret, peer.AddrInfo{
			ID:    remoteID,
			Addrs: []multiaddr.Multiaddr{conn.RemoteMultiaddr()},
		})
		connSet[remoteID.Pretty()] = true
	}
	return ret
}

// skip connecting when 80% peers are connected
func (pm *peerManager) threshold() int {
	return pm.maxPeers * 8 / 10
}

func (pm *peerManager) peerLimit() int {
	if pm.maxPeers == 0 {
		return 0 // unlimited peers
	}
	return pm.maxPeers*12/10 + pm.blacklist.Count()
}
