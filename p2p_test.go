package p2p

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/stretchr/testify/require"
)

func TestBroadcast(t *testing.T) {
	runP2P := func(t *testing.T, options ...Option) {
		ctx := context.Background()
		n := 10
		hosts := make([]*Host, n)
		for i := 0; i < n; i++ {
			opts := []Option{
				Port(30000 + i),
				SecureIO(),
				MasterKey(strconv.Itoa(i)),
			}
			opts = append(opts, options...)
			host, err := NewHost(ctx, opts...)
			require.NoError(t, err)
			require.NoError(t, host.AddBroadcastPubSub(ctx, "test", func(ctx context.Context, data []byte) error {
				fmt.Print(string(data))
				fmt.Printf(", received by %s\n", host.HostIdentity())
				return nil
			}))
			hosts[i] = host
		}

		bootstrapInfo := hosts[0].Info()
		for i := 0; i < n; i++ {
			if i != 0 {
				require.NoError(t, hosts[i].Connect(ctx, bootstrapInfo))
			}
			hosts[i].JoinOverlay()
			err := hosts[i].AdvertiseAsync()
			require.NoError(t, err)
		}

		for i := 0; i < n; i++ {
			require.NoError(
				t,
				hosts[i].Broadcast(ctx, "test", []byte(fmt.Sprintf("msg sent from %s", hosts[i].HostIdentity()))),
			)
		}

		time.Sleep(100 * time.Millisecond)
		for i := 0; i < n; i++ {
			require.NoError(t, hosts[i].Close())
		}
	}

	t.Run("flood", func(t *testing.T) {
		runP2P(t)
	})

	t.Run("gossip", func(t *testing.T) {
		runP2P(t, Gossip())
	})
}

func TestUnicast(t *testing.T) {
	ctx := context.Background()
	var (
		n                  = 10
		hosts              = make([]*Host, n)
		count        int32 = 0
		unicastCount int32 = 0
	)
	for i := 0; i < n; i++ {
		host, err := NewHost(ctx, Port(30000+i), SecureIO(), MasterKey(strconv.Itoa(i)))
		require.NoError(t, err)
		require.NoError(t, host.AddUnicastPubSub("test", func(ctx context.Context, _ peer.AddrInfo, data []byte) error {
			fmt.Print(string(data))
			fmt.Printf(", received by %s\n", host.HostIdentity())
			atomic.AddInt32(&count, 1)
			return nil
		}))
		hosts[i] = host
	}

	bootstrapInfo := hosts[0].Info()
	for i := 0; i < n; i++ {
		if i != 0 {
			require.NoError(t, hosts[i].Connect(ctx, bootstrapInfo))
		}
		hosts[i].JoinOverlay()
		err := hosts[i].AdvertiseAsync()
		require.NoError(t, err)
	}

	for i, host := range hosts {
		neighbors := host.Neighbors(ctx)
		for _, neighbor := range neighbors {
			require.NoError(
				t,
				host.Unicast(ctx, neighbor, "test", []byte(fmt.Sprintf("msg sent from %s", hosts[i].HostIdentity()))),
			)
			atomic.AddInt32(&unicastCount, 1)
		}
	}
	err := waitUntil(100*time.Millisecond, 3*time.Second, func() bool {
		return atomic.LoadInt32(&count) == atomic.LoadInt32(&unicastCount)
	})
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)
	for i := 0; i < n; i++ {
		require.NoError(t, hosts[i].Close())
	}
}

func TestPeerManager(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	var (
		hosts        = []*Host{}
		bootstrap    *Host
		err          error
		n                  = 10
		topic              = "test"
		count        int32 = 0
		unicastCount int32 = 0
	)
	for i := 0; i < n; i++ {
		if i == 0 {
			bootstrap, err = NewHost(ctx, Port(30000+i), SecureIO(), MasterKey(strconv.Itoa(i)))
			require.NoError(err)
			continue
		}
		host, err := NewHost(ctx, Port(30000+i), SecureIO(), MasterKey(strconv.Itoa(i)))
		require.NoError(err)
		require.NoError(host.AddUnicastPubSub(topic, func(ctx context.Context, _ peer.AddrInfo, data []byte) error {
			fmt.Print(string(data))
			fmt.Printf(", received by %s\n", host.HostIdentity())
			atomic.AddInt32(&count, 1)
			return nil
		}))
		hosts = append(hosts, host)
	}

	bootstrapInfo := bootstrap.Info()
	for i := range hosts {
		require.NoError(hosts[i].Connect(ctx, bootstrapInfo))
		require.Equal(network.Connected, hosts[i].host.Network().Connectedness(bootstrapInfo.ID))
	}

	for _, host := range hosts {
		host.JoinOverlay()
		require.NoError(host.AdvertiseAsync())
		require.NoError(host.FindPeersAsync())
	}

	err = waitUntil(100*time.Millisecond, 3*time.Second, func() bool {
		for _, host := range hosts {
			if len(hosts) != len(host.ConnectedPeers()) {
				return false
			}
		}
		return true
	})
	require.NoError(err)

	for _, host := range hosts {
		for _, peer := range host.ConnectedPeers() {
			// skip bootnode
			if peer.ID == bootstrapInfo.ID {
				continue
			}
			require.NoError(
				host.Unicast(ctx, peer, topic, []byte(fmt.Sprintf("msg sent from %s", host.HostIdentity()))),
			)
			atomic.AddInt32(&unicastCount, 1)
		}
	}

	err = waitUntil(100*time.Millisecond, 3*time.Second, func() bool {
		return atomic.LoadInt32(&count) == atomic.LoadInt32(&unicastCount)
	})
	require.NoError(err)
	require.Equal((n-1)*(n-2), int(count))

	for i := range hosts {
		require.NoError(hosts[i].Close())
	}
	require.NoError(bootstrap.Close())
}

func TestAddBootNode(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	var (
		hosts     = []*Host{}
		bootstrap *Host
		err       error
		n         = 10
	)
	for i := 0; i < n; i++ {
		if i == 0 {
			bootstrap, err = NewHost(ctx, Port(30000+i), SecureIO(), MasterKey(strconv.Itoa(i)))
			require.NoError(err)
			continue
		}
		host, err := NewHost(ctx, Port(30000+i), SecureIO(), MasterKey(strconv.Itoa(i)))
		require.NoError(err)
		hosts = append(hosts, host)
	}

	bootstrapInfo := bootstrap.Info()
	for i := range hosts {
		require.NoError(hosts[i].Connect(ctx, bootstrapInfo))
		require.Equal(network.Connected, hosts[i].host.Network().Connectedness(bootstrapInfo.ID))
	}

	for _, host := range hosts {
		host.JoinOverlay()
		require.NoError(host.AdvertiseAsync())
		require.NoError(host.FindPeersAsync())

		bAddr, err := peer.AddrInfoToP2pAddrs(&bootstrapInfo)
		require.NoError(err)
		require.NoError(host.AddBootstrap(bAddr))
	}

	err = waitUntil(100*time.Millisecond, 3*time.Second, func() bool {
		for _, host := range hosts {
			if n-2 != len(host.ConnectedPeers()) {
				return false
			}
		}
		return true
	})
	require.NoError(err)

	for i := range hosts {
		require.NoError(hosts[i].Close())
	}
	require.NoError(bootstrap.Close())
}

func TestBlacklist(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	var (
		hosts = []*Host{}
		n     = 10
	)
	for i := 0; i < n; i++ {
		host, err := NewHost(ctx, Port(30000+i), SecureIO(), MasterKey(strconv.Itoa(i)))
		require.NoError(err)
		hosts = append(hosts, host)
	}

	for _, host := range hosts {
		host.JoinOverlay()
		require.NoError(host.AdvertiseAsync())
	}

	id1 := hosts[1].host.ID()
	id2 := hosts[2].host.ID()
	pm0 := hosts[0].peerManager

	pm0.TryBlockPeer(id1)
	require.False(pm0.Blocked(id1))
	pm0.TryBlockPeer(id1)
	require.False(pm0.Blocked(id1))
	pm0.TryBlockPeer(id1)
	require.True(pm0.Blocked(id1))

	pm0.BlockPeer(id2)
	require.True(pm0.Blocked(id2))

	for i := range hosts {
		require.NoError(hosts[i].Close())
	}
}

func TestConnect(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	var (
		n         = 10
		hosts     = make([]*Host, n)
		addrInfos = make([]peer.AddrInfo, n)
	)

	for i := 0; i < n; i++ {
		host, err := NewHost(ctx, Port(30000+i), SecureIO(), MasterKey(strconv.Itoa(i)))
		require.NoError(err)
		hosts[i] = host
		addrInfos[i] = peer.AddrInfo{
			ID:    host.host.ID(),
			Addrs: host.host.Addrs(),
		}
	}

	t.Run("oneConn", func(t *testing.T) {
		err := hosts[1].host.Connect(ctx, addrInfos[2])
		require.NoError(err)

		require.Equal(1, len(hosts[1].host.Network().Conns()))
	})

	t.Run("BiDirectConn", func(t *testing.T) {
		err := hosts[1].host.Connect(ctx, addrInfos[2])
		require.NoError(err)
		err = hosts[2].host.Connect(ctx, addrInfos[1])
		require.NoError(err)

		require.Equal(1, len(hosts[1].host.Network().Conns()))
		require.Equal(1, len(hosts[2].host.Network().Conns()))
	})

	t.Run("DoubleConn", func(t *testing.T) {
		err := hosts[1].host.Connect(ctx, addrInfos[2])
		require.NoError(err)
		err = hosts[1].host.Connect(ctx, addrInfos[2])
		require.NoError(err)

		require.Equal(1, len(hosts[1].host.Network().Conns()))
	})

	t.Run("Disconnect", func(t *testing.T) {
		err := hosts[1].host.Connect(ctx, addrInfos[2])
		require.NoError(err)
		err = hosts[1].host.Connect(ctx, addrInfos[3])
		require.NoError(err)
		require.Equal(2, len(hosts[1].host.Network().Conns()))
		err = hosts[3].host.Close()
		require.NoError(err)
		require.Equal(1, len(hosts[1].host.Network().Conns()))
	})

	time.Sleep(100 * time.Millisecond)
	for i := 0; i < n; i++ {
		require.NoError(hosts[i].Close())
	}
}

func TestStream(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	var (
		n         = 10
		hosts     = make([]*Host, n)
		addrInfos = make([]peer.AddrInfo, n)
		topic1    = "topic1"
		topic2    = "topic2"
		countMap  map[string]int32

		mu = sync.RWMutex{}
	)

	for i := 0; i < n; i++ {
		host, err := NewHost(ctx, Port(30000+i), SecureIO(), MasterKey(strconv.Itoa(i)))
		require.NoError(err)
		require.NoError(host.AddUnicastPubSub(topic1, func(ctx context.Context, _ peer.AddrInfo, data []byte) error {
			mu.Lock()
			defer mu.Unlock()
			countMap[string(data)]++
			return nil
		}))
		require.NoError(host.AddUnicastPubSub(topic2, func(ctx context.Context, _ peer.AddrInfo, data []byte) error {
			mu.Lock()
			defer mu.Unlock()
			countMap[string(data)]++
			return nil
		}))
		hosts[i] = host
		addrInfos[i] = peer.AddrInfo{
			ID:    host.host.ID(),
			Addrs: host.host.Addrs(),
		}
	}

	t.Run("oneStream", func(t *testing.T) {
		countMap = make(map[string]int32)
		data := "test1"

		err := hosts[1].host.Connect(ctx, addrInfos[2])
		require.NoError(err)
		s, err := hosts[1].host.NewStream(ctx, addrInfos[2].ID, protocol.ID(topic1))
		require.NoError(err)
		s.Write([]byte(data))
		s.Close()

		err = waitUntil(100*time.Millisecond, 3*time.Second, func() bool {
			mu.RLock()
			defer mu.RUnlock()
			return countMap[data] == 1
		})
		require.NoError(err)
	})

	t.Run("twoStreams", func(t *testing.T) {
		countMap = make(map[string]int32)
		data := "test2"

		err := hosts[1].host.Connect(ctx, addrInfos[2])
		require.NoError(err)
		err = hosts[3].host.Connect(ctx, addrInfos[2])
		require.NoError(err)
		s, err := hosts[1].host.NewStream(ctx, addrInfos[2].ID, protocol.ID(topic1))
		require.NoError(err)
		s.Write([]byte(data))
		s2, err := hosts[3].host.NewStream(ctx, addrInfos[2].ID, protocol.ID(topic1))
		require.NoError(err)
		s2.Write([]byte(data))
		s.Close()
		s2.Close()

		err = waitUntil(100*time.Millisecond, 3*time.Second, func() bool {
			mu.RLock()
			defer mu.RUnlock()
			return countMap[data] == 2
		})
		require.NoError(err)
	})

	t.Run("twoStreamsWithDifferentTopic", func(t *testing.T) {
		countMap = make(map[string]int32)
		data := "test3"

		err := hosts[1].host.Connect(ctx, addrInfos[2])
		require.NoError(err)
		err = hosts[3].host.Connect(ctx, addrInfos[2])
		require.NoError(err)
		s, err := hosts[1].host.NewStream(ctx, addrInfos[2].ID, protocol.ID(topic1))
		require.NoError(err)
		s.Write([]byte(data))
		s2, err := hosts[3].host.NewStream(ctx, addrInfos[2].ID, protocol.ID(topic2))
		require.NoError(err)
		s2.Write([]byte(data))
		s.Close()
		s2.Close()

		err = waitUntil(100*time.Millisecond, 3*time.Second, func() bool {
			mu.RLock()
			defer mu.RUnlock()
			return countMap[data] == 2
		})
		require.NoError(err)
	})

	t.Run("twoStreamsBidirect", func(t *testing.T) {
		countMap = make(map[string]int32)
		data := "test4"

		err := hosts[1].host.Connect(ctx, addrInfos[2])
		require.NoError(err)
		s, err := hosts[1].host.NewStream(ctx, addrInfos[2].ID, protocol.ID(topic1))
		require.NoError(err)
		s.Write([]byte(data))
		s2, err := hosts[2].host.NewStream(ctx, addrInfos[1].ID, protocol.ID(topic2))
		require.NoError(err)
		s2.Write([]byte(data))
		s.Close()
		s2.Close()

		err = waitUntil(100*time.Millisecond, 3*time.Second, func() bool {
			mu.RLock()
			defer mu.RUnlock()
			return countMap[data] == 2
		})
		require.NoError(err)
	})

	t.Run("oneStreamDoubleWrite", func(t *testing.T) {
		countMap = make(map[string]int32)
		data := "test5"

		err := hosts[1].host.Connect(ctx, addrInfos[2])
		require.NoError(err)
		s, err := hosts[1].host.NewStream(ctx, addrInfos[2].ID, protocol.ID(topic1))
		require.NoError(err)
		_, err = s.Write([]byte(data))
		require.NoError(err)
		_, err = s.Write([]byte(data))
		require.NoError(err)
		s.Close()
		err = waitUntil(100*time.Millisecond, 3*time.Second, func() bool {
			mu.RLock()
			defer mu.RUnlock()
			return countMap[data+data] == 1
		})
		require.NoError(err)
	})

	t.Run("oneStreamDoubleClose", func(t *testing.T) {
		countMap = make(map[string]int32)
		data := "test6"

		err := hosts[1].host.Connect(ctx, addrInfos[2])
		require.NoError(err)
		s, err := hosts[1].host.NewStream(ctx, addrInfos[2].ID, protocol.ID(topic1))
		require.NoError(err)
		s.Write([]byte(data))
		err = s.Close()
		require.NoError(err)
		err = s.Close()
		require.NoError(err)

		err = waitUntil(100*time.Millisecond, 3*time.Second, func() bool {
			mu.RLock()
			defer mu.RUnlock()
			return countMap[data] == 1
		})
		require.NoError(err)
	})

	time.Sleep(100 * time.Millisecond)
	for i := 0; i < n; i++ {
		require.NoError(hosts[i].Close())
	}
}

func TestConnectedPeersByTopic(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()

	var err error
	n := 5
	hosts := make([]*Host, n)
	for i := range hosts {
		hosts[i], err = NewHost(ctx, Port(3000+i), SecureIO(), MasterKey(strconv.Itoa(i)))
		require.NoError(err)
	}
	for i := 1; i < n; i++ {
		require.NoError(hosts[i].Connect(ctx, hosts[0].Info()))
		hosts[i].JoinOverlay()
	}
	callback := func(ctx context.Context, data []byte) error {
		return nil
	}
	require.NoError(hosts[1].AddBroadcastPubSub(ctx, "0", callback))
	require.NoError(hosts[2].AddBroadcastPubSub(ctx, "1", callback))
	require.NoError(hosts[3].AddBroadcastPubSub(ctx, "1", callback))
	require.NoError(hosts[4].AddBroadcastPubSub(ctx, "1", callback))

	time.Sleep(100 * time.Millisecond)
	peers := hosts[1].ConnectedPeersByTopic("0")
	require.Len(peers, 0)
	peers = hosts[1].ConnectedPeersByTopic("1")
	require.Len(peers, 3)
	peers = hosts[2].ConnectedPeersByTopic("0")
	require.Len(peers, 1)
	peers = hosts[2].ConnectedPeersByTopic("1")
	require.Len(peers, 2)
}

func TestBroadcastMultipleTopic(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()

	var err error
	n := 5
	hosts := make([]*Host, n)
	for i := range hosts {
		hosts[i], err = NewHost(ctx, Port(3000+i), SecureIO(), MasterKey(strconv.Itoa(i)))
		require.NoError(err)
	}
	for i := 1; i < n; i++ {
		require.NoError(hosts[i].Connect(ctx, hosts[0].Info()))
		hosts[i].JoinOverlay()
	}
	count := map[int]int{}
	mutex := sync.Mutex{}
	resetCount := func() {
		for i := range count {
			count[i] = 0
		}
	}
	callback := func(i int) HandleBroadcast {
		return func(ctx context.Context, data []byte) error {
			mutex.Lock()
			defer mutex.Unlock()
			count[i]++
			return nil
		}
	}
	require.NoError(hosts[1].AddBroadcastPubSub(ctx, "consensus", callback(1)))
	require.NoError(hosts[1].AddBroadcastPubSub(ctx, "block", callback(1)))
	require.NoError(hosts[2].AddBroadcastPubSub(ctx, "block", callback(2)))
	require.NoError(hosts[2].AddBroadcastPubSub(ctx, "action", callback(2)))
	require.NoError(hosts[3].AddBroadcastPubSub(ctx, "block", callback(3)))
	require.NoError(hosts[3].AddBroadcastPubSub(ctx, "action", callback(3)))

	time.Sleep(100 * time.Millisecond)

	t.Run("broadcastSubscribedTopic", func(t *testing.T) {
		resetCount()
		require.NoError(hosts[3].Broadcast(ctx, "action", []byte("")))
		require.NoError(waitUntil(100*time.Millisecond, time.Second, func() bool {
			mutex.Lock()
			defer mutex.Unlock()
			return 1 == count[2] && 0 == count[1]
		}))
		resetCount()
		require.NoError(hosts[3].Broadcast(ctx, "block", []byte("")))
		require.NoError(waitUntil(100*time.Millisecond, time.Second, func() bool {
			mutex.Lock()
			defer mutex.Unlock()
			return 1 == count[2] && 1 == count[1]
		}))
	})
	t.Run("broadcastUnsubscribedTopic", func(t *testing.T) {
		resetCount()
		require.NoError(hosts[3].Broadcast(ctx, "consensus", []byte("")))
		require.NoError(waitUntil(100*time.Millisecond, time.Second, func() bool {
			mutex.Lock()
			defer mutex.Unlock()
			return 0 == count[2] && 1 == count[1]
		}))
	})
	t.Run("broadcastUnsubscribedTopicWithNoPeers", func(t *testing.T) {
		resetCount()
		err := hosts[3].Broadcast(ctx, "unknown", []byte(""))
		require.True(errors.Is(err, ErrNoConnectedPeers))
	})
}

func waitUntil(interval time.Duration, timeout time.Duration, cond func() bool) error {
	ticker := time.NewTicker(interval)
	timer := time.NewTimer(timeout)
	for {
		select {
		case <-timer.C:
			return errors.New("timeout")
		case <-ticker.C:
			if cond() {
				return nil
			}
		}
	}
}
