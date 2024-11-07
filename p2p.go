package p2p

import (
	"context"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"strconv"
	"time"

	lru "github.com/hashicorp/golang-lru"
	"github.com/ipfs/go-cid"
	"github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multihash"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	"golang.org/x/time/rate"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/pnet"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
	yamux "github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	connmgr "github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
)

type (
	// HandleBroadcast defines the callback function triggered when a broadcast message reaches a host
	HandleBroadcast func(ctx context.Context, data []byte) error

	// HandleUnicast defines the callback function triggered when a unicast message reaches a host
	HandleUnicast func(context.Context, peer.AddrInfo, []byte) error

	// Config enumerates the configs required by a host
	Config struct {
		ProtocolID               protocol.ID     `yaml:"protocolID "`
		HostName                 string          `yaml:"hostName"`
		Port                     int             `yaml:"port"`
		ExternalHostName         string          `yaml:"externalHostName"`
		ExternalPort             int             `yaml:"externalPort"`
		SecureIO                 bool            `yaml:"secureIO"`
		Gossip                   bool            `yaml:"gossip"`
		ConnectTimeout           time.Duration   `yaml:"connectTimeout"`
		BlackListTimeout         time.Duration   `yaml:"blackListTimeout"`
		MasterKey                string          `yaml:"masterKey"`
		Relay                    string          `yaml:"relay"` // could be `active`, `nat`, `disable`
		ConnLowWater             int             `yaml:"connLowWater"`
		ConnHighWater            int             `yaml:"connHighWater"`
		RateLimiterLRUSize       int             `yaml:"rateLimiterLRUSize"`
		BlockListLRUSize         int             `yaml:"blockListLRUSize"`
		BlockListErrorThreshold  int             `yaml:"blockListErrorThreshold"`
		BlockListCleanupInterval time.Duration   `yaml:"blockListCleanupInterval"`
		ConnGracePeriod          time.Duration   `yaml:"connGracePeriod"`
		EnableRateLimit          bool            `yaml:"enableRateLimit"`
		RateLimit                RateLimitConfig `yaml:"rateLimit"`
		PrivateNetworkPSK        string          `yaml:"privateNetworkPSK"`
		GroupID                  string          `yaml:"groupID"`
		MaxPeer                  int             `yaml:"maxPeer"`
		MaxMessageSize           int             `yaml:"maxMessageSize"`
		BlacklistTolerance       int             `yaml:"blacklistTolerance"`
	}

	// RateLimitConfig all numbers are per second value.
	RateLimitConfig struct {
		GlobalUnicastAvg   int `yaml:"globalUnicastAvg"`
		GlobalUnicastBurst int `yaml:"globalUnicastBurst"`
		PeerAvg            int `yaml:"peerAvg"`
		PeerBurst          int `yaml:"peerBurst"`
	}
)

var (
	// DefaultConfig is a set of default configs
	DefaultConfig = Config{
		HostName:                 "127.0.0.1",
		Port:                     30001,
		ExternalHostName:         "",
		ExternalPort:             30001,
		SecureIO:                 false,
		Gossip:                   false,
		ConnectTimeout:           time.Minute,
		BlackListTimeout:         10 * time.Minute,
		MasterKey:                "",
		Relay:                    "disable",
		ConnLowWater:             200,
		ConnHighWater:            500,
		RateLimiterLRUSize:       1000,
		BlockListLRUSize:         1000,
		BlockListErrorThreshold:  3,
		BlockListCleanupInterval: 600 * time.Second,
		ConnGracePeriod:          0,
		EnableRateLimit:          false,
		RateLimit:                DefaultRatelimitConfig,
		PrivateNetworkPSK:        "",
		ProtocolID:               "/iotex",
		GroupID:                  "iotex",
		MaxPeer:                  30,
		MaxMessageSize:           12582912, // 12MB
		BlacklistTolerance:       3,
	}

	// DefaultRatelimitConfig is the default rate limit config
	DefaultRatelimitConfig = RateLimitConfig{
		GlobalUnicastAvg:   300,
		GlobalUnicastBurst: 500,
		PeerAvg:            300,
		PeerBurst:          500,
	}
)

// Option defines the option function to modify the config for a host
type Option func(cfg *Config) error

// HostName is the option to override the host name or IP address
func HostName(hostName string) Option {
	return func(cfg *Config) error {
		cfg.HostName = hostName
		return nil
	}
}

// Port is the option to override the port number
func Port(port int) Option {
	return func(cfg *Config) error {
		cfg.Port = port
		return nil
	}
}

// ExternalHostName is the option to set the host name or IP address seen from external
func ExternalHostName(externalHostName string) Option {
	return func(cfg *Config) error {
		cfg.ExternalHostName = externalHostName
		return nil
	}
}

// ExternalPort is the option to set the port number seen from external
func ExternalPort(externalPort int) Option {
	return func(cfg *Config) error {
		cfg.ExternalPort = externalPort
		return nil
	}
}

// SecureIO is to indicate using secured I/O
func SecureIO() Option {
	return func(cfg *Config) error {
		cfg.SecureIO = true
		return nil
	}
}

// Gossip is to indicate using gossip protocol
func Gossip() Option {
	return func(cfg *Config) error {
		cfg.Gossip = true
		return nil
	}
}

// ConnectTimeout is the option to override the connect timeout
func ConnectTimeout(timout time.Duration) Option {
	return func(cfg *Config) error {
		cfg.ConnectTimeout = timout
		return nil
	}
}

// MasterKey is to determine network identifier
func MasterKey(masterKey string) Option {
	return func(cfg *Config) error {
		cfg.MasterKey = masterKey
		return nil
	}
}

// WithRateLimit is to indicate limiting msg rate from peers
func WithRateLimit(rcfg RateLimitConfig) Option {
	return func(cfg *Config) error {
		cfg.EnableRateLimit = true
		cfg.RateLimit = rcfg
		return nil
	}
}

// WithRelay config relay option.
func WithRelay(relayType string) Option {
	return func(cfg *Config) error {
		cfg.Relay = relayType
		return nil
	}
}

// WithConnectionManagerConfig set configuration for connection manager.
func WithConnectionManagerConfig(lo, hi int, grace time.Duration) Option {
	return func(cfg *Config) error {
		cfg.ConnLowWater = lo
		cfg.ConnHighWater = hi
		cfg.ConnGracePeriod = grace
		return nil
	}
}

// PrivateNetworkPSK is to determine network identifier
func PrivateNetworkPSK(privateNetworkPSK string) Option {
	return func(cfg *Config) error {
		cfg.PrivateNetworkPSK = privateNetworkPSK
		return nil
	}
}

// DHTProtocolID returns the prefix of dht protocol.
// MainNet uses "/iotex", while other networks use "/iotex*"(e.g. "/iotex2", "iotex3")
func DHTProtocolID(chainID uint32) Option {
	return func(cfg *Config) error {
		if chainID != 1 {
			cfg.ProtocolID = protocol.ID("/iotex" + strconv.Itoa(int(chainID)))
		}
		return nil
	}
}

// DHTGroupID helps node finds peers of the same GroupID in dht discovering.
// MainNet uses "iotex", while other networks use "iotex*"(e.g. "iotex2", "iotex3")
func DHTGroupID(chainID uint32) Option {
	return func(cfg *Config) error {
		if chainID != 1 {
			cfg.GroupID = "iotex" + strconv.Itoa(int(chainID))
		}
		return nil
	}
}

// WithMaxPeer config MaxPeer option.
func WithMaxPeer(num uint32) Option {
	return func(cfg *Config) error {
		cfg.MaxPeer = int(num)
		return nil
	}
}

// WithMaxMessageSize config MaxMessageSize option.
func WithMaxMessageSize(size int) Option {
	return func(cfg *Config) error {
		cfg.MaxMessageSize = size
		return nil
	}
}

// Host is the main struct that represents a host that communicating with the rest of the P2P networks
type Host struct {
	host           core.Host
	cfg            Config
	topics         map[string]bool
	kad            *dht.IpfsDHT
	kadKey         cid.Cid
	pubsub         *pubsub.PubSub
	pubs           map[string]*pubsub.Topic
	blacklist      *LRUBlacklist
	subs           map[string]*pubsub.Subscription
	close          chan interface{}
	ctx            context.Context
	peersLimiters  *lru.Cache
	unicastLimiter *rate.Limiter
	peerManager    *peerManager
}

// NewHost constructs a host struct
func NewHost(ctx context.Context, options ...Option) (*Host, error) {
	cfg := DefaultConfig
	for _, option := range options {
		if err := option(&cfg); err != nil {
			return nil, err
		}
	}
	ip, err := EnsureIPv4(cfg.HostName)
	if err != nil {
		return nil, err
	}
	var (
		extMultiAddr multiaddr.Multiaddr
		masterKey    = cfg.MasterKey
	)
	// Set external address and replace private key it external host name is given
	if cfg.ExternalHostName != "" {
		// If ID is not given use network address instead
		if masterKey == "" {
			masterKey = fmt.Sprintf("%s:%d", cfg.ExternalHostName, cfg.ExternalPort)
		}
		extIP, err := EnsureIPv4(cfg.ExternalHostName)
		if err != nil {
			return nil, err
		}
		extMultiAddr, err = multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/tcp/%d", extIP, cfg.ExternalPort))
		if err != nil {
			return nil, err
		}
	} else {
		// If ID is not given use network address instead
		if masterKey == "" {
			masterKey = fmt.Sprintf("%s:%d", ip, cfg.Port)
		}
	}
	prikey, _, err := generateKeyPair(masterKey)
	if err != nil {
		return nil, err
	}
	connMgr, err := connmgr.NewConnManager(cfg.ConnLowWater, cfg.ConnHighWater, connmgr.WithGracePeriod(cfg.ConnGracePeriod))
	if err != nil {
		return nil, err
	}
	opts := []libp2p.Option{
		libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/%s/tcp/%d", ip, cfg.Port)),
		libp2p.AddrsFactory(func(addrs []multiaddr.Multiaddr) []multiaddr.Multiaddr {
			if extMultiAddr != nil {
				return append(addrs, extMultiAddr)
			}
			return addrs
		}),
		libp2p.Identity(prikey),
		libp2p.DefaultSecurity,
		libp2p.Transport(func(upgrader transport.Upgrader) *tcp.TcpTransport {
			tp, err := tcp.NewTCPTransport(upgrader, &network.NullResourceManager{}, tcp.WithConnectionTimeout(cfg.ConnectTimeout))
			if err != nil {
				Logger().Panic("failed to new tcp transport", zap.Error(err))
			}
			return tp
		}),
		libp2p.Muxer("/yamux/2.0.0", yamux.DefaultTransport),
		libp2p.ConnectionManager(connMgr),
		libp2p.DefaultPrometheusRegisterer,
	}

	if !cfg.SecureIO {
		opts = append(opts, libp2p.NoSecurity)
	}

	// relay option
	if cfg.Relay == "active" {
		opts = append(opts, libp2p.EnableRelay())
	} else if cfg.Relay == "nat" {
		opts = append(opts, libp2p.EnableRelay(), libp2p.NATPortMap())
	} else {
		opts = append(opts, libp2p.DisableRelay())
	}

	// private p2p network
	if cfg.PrivateNetworkPSK != "" {
		f, err := os.Open(cfg.PrivateNetworkPSK)
		if err != nil {
			return nil, err
		}
		p, err := pnet.DecodeV1PSK(f)
		if err != nil {
			return nil, err
		}
		opts = append(opts, libp2p.PrivateNetwork(p))
	}

	host, err := libp2p.New(opts...)
	if err != nil {
		return nil, err
	}
	kad, err := dht.New(ctx, host, dht.ProtocolPrefix(cfg.ProtocolID), dht.Mode(dht.ModeServer))
	if err != nil {
		return nil, err
	}
	if err := kad.Bootstrap(ctx); err != nil {
		return nil, err
	}
	newPubSub := pubsub.NewFloodSub
	if cfg.Gossip {
		newPubSub = pubsub.NewGossipSub
	}
	v1b := cid.V1Builder{Codec: cid.Raw, MhType: multihash.SHA2_256}
	cid, err := v1b.Sum([]byte(masterKey))
	if err != nil {
		return nil, err
	}
	limiters, err := lru.New(cfg.RateLimiterLRUSize)
	if err != nil {
		return nil, err
	}
	blacklist, err := NewLRUBlacklist(cfg.BlockListLRUSize)
	if err != nil {
		return nil, err
	}
	ps, err := newPubSub(ctx, host, pubsub.WithBlacklist(blacklist), pubsub.WithMaxMessageSize(cfg.MaxMessageSize))
	if err != nil {
		return nil, err
	}
	myHost := Host{
		host:           host,
		cfg:            cfg,
		topics:         make(map[string]bool),
		kad:            kad,
		kadKey:         cid,
		pubsub:         ps,
		pubs:           make(map[string]*pubsub.Topic),
		blacklist:      blacklist,
		subs:           make(map[string]*pubsub.Subscription),
		close:          make(chan interface{}),
		ctx:            ctx,
		peersLimiters:  limiters,
		unicastLimiter: rate.NewLimiter(rate.Limit(cfg.RateLimit.GlobalUnicastAvg), cfg.RateLimit.GlobalUnicastBurst),
		peerManager: newPeerManager(host, routing.NewRoutingDiscovery(kad), cfg.GroupID,
			withMaxPeers(cfg.MaxPeer), withBlacklistTolerance(cfg.BlacklistTolerance), withBlacklistTimeout(cfg.BlackListTimeout)),
	}

	addrs := make([]string, 0)
	for _, ma := range myHost.Addresses() {
		addrs = append(addrs, ma.String())
	}
	Logger().Info("P2p host started.",
		zap.Strings("address", addrs),
		zap.Bool("secureIO", myHost.cfg.SecureIO),
		zap.Bool("gossip", myHost.cfg.Gossip))
	return &myHost, nil
}

// JoinOverlay triggers the host to join the DHT overlay
func (h *Host) JoinOverlay() {
	h.peerManager.JoinOverlay()
}

// AdvertiseAsync publish the groupID of host to the dht network
func (h *Host) AdvertiseAsync() error {
	return h.peerManager.Advertise()
}

// FindPeersAsync connect host to the peers with the same groupID
func (h *Host) FindPeersAsync() error {
	return h.peerManager.ConnectPeers()
}

// AddUnicastPubSub adds a unicast topic that the host will pay attention to
func (h *Host) AddUnicastPubSub(topic string, callback HandleUnicast) error {
	if h.topics[topic] {
		return nil
	}
	h.host.SetStreamHandler(protocol.ID(topic), func(stream core.Stream) {
		conn := stream.Conn()
		if h.cfg.EnableRateLimit && !h.unicastLimiter.Allow() {
			Logger().Warn("Drop unicast stream due to high traffic volume.", zap.String("id", conn.RemotePeer().String()))
			return
		}
		/*
			src := stream.Conn().RemotePeer()
			allowed, err := h.allowSource(src)
			if err != nil {
				Logger().Error("Error when checking if the source is allowed.", zap.Error(err))
				return
			}
			if !allowed {
				// TODO: blacklist src for unicast too
				return
			}
		*/
		data, err := ioutil.ReadAll(stream)
		if err != nil {
			Logger().Error("Error when subscribing a unicast message.", zap.Error(err))
			return
		}

		RemoteAddr := peer.AddrInfo{
			ID:    conn.RemotePeer(),
			Addrs: []multiaddr.Multiaddr{conn.RemoteMultiaddr()},
		}

		if err := stream.Close(); err != nil {
			stream.Reset()
		}

		if err := callback(context.Background(), RemoteAddr, data); err != nil {
			Logger().Error("Error when processing a unicast message.", zap.Error(err))
		}
	})
	h.topics[topic] = true
	return nil
}

// AddBroadcastPubSub adds a broadcast topic that the host will pay attention to. This need to be called before using
// Connect/JoinOverlay. Otherwise, pubsub may not be aware of the existing overlay topology
func (h *Host) AddBroadcastPubSub(ctx context.Context, topic string, callback HandleBroadcast) error {
	if _, ok := h.pubs[topic]; ok {
		return nil
	}
	top, err := h.pubsub.Join(topic)
	if err != nil {
		return err
	}
	sub, err := top.Subscribe()
	if err != nil {
		return err
	}
	h.pubs[topic] = top
	h.subs[topic] = sub
	go func() {
		for {
			select {
			case <-h.close:
				return
			case <-ctx.Done():
				return
			default:
				msg, err := sub.Next(ctx)
				if err != nil {
					Logger().Error("Error when subscribing a broadcast message.", zap.Error(err))
					continue
				}
				src := msg.GetFrom()
				allowed, err := h.allowSource(src)
				if err != nil {
					Logger().Error("Error when checking if the source is allowed.", zap.Error(err))
					continue
				}
				if !allowed {
					h.blacklist.Add(src)
					Logger().Warn("Blacklist a peer", zap.String("id", src.String()))
					continue
				}
				h.blacklist.Remove(src)
				bctx := context.WithValue(ctx, broadcastCtxKey{}, msg)
				if err := callback(bctx, msg.Data); err != nil {
					Logger().Error("Error when processing a broadcast message.", zap.Error(err))
				}
			}
		}
	}()
	go func() {
		for {
			select {
			case <-h.close:
				return
			case <-ctx.Done():
				return
			default:
				time.Sleep(h.cfg.BlockListCleanupInterval)
				h.blacklist.RemoveOldest()
			}
		}
	}()
	return nil
}

// ConnectWithMultiaddr connects a peer given the multi address
func (h *Host) ConnectWithMultiaddr(ctx context.Context, ma multiaddr.Multiaddr) error {
	target, err := peer.AddrInfoFromP2pAddr(ma)
	if err != nil {
		return err
	}
	if err := h.host.Connect(ctx, *target); err != nil {
		return err
	}
	Logger().Debug(
		"P2P peer connected.",
		zap.String("multiAddress", ma.String()),
	)
	return nil
}

// Connect connects a peer.
func (h *Host) Connect(ctx context.Context, target core.PeerAddrInfo) error {
	if err := h.host.Connect(ctx, target); err != nil {
		return err
	}
	Logger().Debug(
		"P2P peer connected.",
		zap.String("peer", fmt.Sprintf("%+v", target)),
	)
	return nil
}

// Broadcast sends a message to the hosts who subscribe the topic
func (h *Host) Broadcast(ctx context.Context, topic string, data []byte) error {
	pub, ok := h.pubs[topic]
	if !ok {
		return nil
	}
	return pub.Publish(ctx, data)
}

// Unicast sends a message to a peer on the given address
func (h *Host) Unicast(ctx context.Context, target core.PeerAddrInfo, topic string, data []byte) error {
	if err := h.unicast(ctx, target, topic, data); err != nil {
		Logger().Error("Error when sending a unicast message.", zap.Error(err))
		h.peerManager.TryBlockPeer(target.ID)
		return err
	}
	return nil
}

func (h *Host) unicast(ctx context.Context, target core.PeerAddrInfo, topic string, data []byte) error {
	if err := h.host.Connect(ctx, target); err != nil {
		return errors.Wrap(err, "failed to connect the peer")
	}
	stream, err := h.host.NewStream(ctx, target.ID, protocol.ID(topic))
	if err != nil {
		return errors.Wrap(err, "failed to build stream with the peer")
	}
	if _, err = stream.Write(data); err != nil {
		stream.Reset()
		return errors.Wrap(err, "failed to write stream")
	}
	return stream.Close()
}

// ClearBlocklist clears the blocklist
func (h *Host) ClearBlocklist() {
	h.peerManager.ClearBlockList()
}

// BlockPeer add the peer into the blocklist
func (h *Host) BlockPeer(id peer.ID) {
	h.peerManager.BlockPeer(id)
}

// AddBootstrap adds bootnode into peermanager
func (h *Host) AddBootstrap(bootNodeAddrs []multiaddr.Multiaddr) error {
	for _, node := range bootNodeAddrs {
		addr, err := peer.AddrInfoFromP2pAddr(node)
		if err != nil {
			return err
		}
		h.peerManager.AddBootstrap(addr.ID)
	}
	return nil
}

// HostIdentity returns the host identity string
func (h *Host) HostIdentity() string { return h.host.ID().String() }

// OverlayIdentity returns the overlay identity string
func (h *Host) OverlayIdentity() string { return h.kadKey.String() }

// Addresses returns the multi address
func (h *Host) Addresses() []multiaddr.Multiaddr {
	hostID, _ := multiaddr.NewMultiaddr(fmt.Sprintf("/ipfs/%s", h.HostIdentity()))
	addrs := make([]multiaddr.Multiaddr, 0)
	for _, addr := range h.host.Addrs() {
		addrs = append(addrs, addr.Encapsulate(hostID))
	}
	return addrs
}

// Info returns host's perr info.
func (h *Host) Info() core.PeerAddrInfo {
	return core.PeerAddrInfo{ID: h.host.ID(), Addrs: h.host.Addrs()}
}

// Neighbors returns the closest peer addresses
func (h *Host) Neighbors(ctx context.Context) []core.PeerAddrInfo {
	var (
		dedup     = make(map[string]bool)
		neighbors = make([]core.PeerAddrInfo, 0)
	)
	for _, p := range h.host.Peerstore().Peers() {
		idStr := p.String()
		if dedup[idStr] || idStr == h.host.ID().String() || idStr == "" {
			continue
		}
		dedup[idStr] = true
		peer := h.kad.FindLocal(ctx, p)
		if peer.ID != "" && len(peer.Addrs) > 0 && !h.peerManager.Blocked(peer.ID) {
			neighbors = append(neighbors, peer)
		}
	}
	return neighbors
}

// ConnectedPeers returns the connected peers' addrinfo
func (h *Host) ConnectedPeers() []core.PeerAddrInfo {
	return h.peerManager.ConnectedPeers()
}

// Close closes the host
func (h *Host) Close() error {
	for _, sub := range h.subs {
		sub.Cancel()
	}
	if err := h.kad.Close(); err != nil {
		return err
	}
	if err := h.host.Close(); err != nil {
		return err
	}
	close(h.close)
	return nil
}

func (h *Host) allowSource(src core.PeerID) (bool, error) {
	if !h.cfg.EnableRateLimit {
		return true, nil
	}
	var limiter *rate.Limiter
	val, ok := h.peersLimiters.Get(src)
	if ok {
		limiter, ok = val.(*rate.Limiter)
		if !ok {
			return false, errors.New("error when casting to limiter struct")
		}
	} else {
		limiter = rate.NewLimiter(rate.Limit(h.cfg.RateLimit.PeerAvg), h.cfg.RateLimit.PeerBurst)
		h.peersLimiters.Add(src, limiter)
	}
	return limiter.Allow(), nil
}

// generateKeyPair generates the public key and private key by network address
func generateKeyPair(masterKey string) (crypto.PrivKey, crypto.PubKey, error) {
	hash := sha1.Sum([]byte(masterKey))
	seedBytes := hash[12:]
	seedBytes[0] = 0
	seed := int64(binary.BigEndian.Uint64(seedBytes))
	r := rand.New(rand.NewSource(seed))
	return crypto.GenerateKeyPairWithReader(crypto.Ed25519, 2048, r)
}
