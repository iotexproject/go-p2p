package p2p

import (
	"context"
	"fmt"
	"io/ioutil"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/multiformats/go-multihash"
	"github.com/stretchr/testify/assert"

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
			require.NoError(t, host.AddBroadcastPubSub("test", func(ctx context.Context, data []byte) error {
				fmt.Print(string(data))
				fmt.Printf(", received by %s\n", host.HostIdentity())
				return nil
			}))
			hosts[i] = host
		}
		time.Sleep(5 * time.Second)

		bootstrapInfo := hosts[0].Info()
		for i := 0; i < n; i++ {
			if i != 0 {
				require.NoError(t, hosts[i].Connect(ctx, bootstrapInfo))
			}
			go hosts[i].Discover(ctx, hosts[i].DHT(), "huski-network")
		}

		for i := 0; i < n; i++ {
			require.NoError(
				t,
				hosts[i].Broadcast("test", []byte(fmt.Sprintf("msg sent from %s", hosts[i].HostIdentity()))),
			)
		}

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
	n := 10
	hosts := make([]*Host, n)
	for i := 0; i < n; i++ {
		host, err := NewHost(ctx, Port(30000+i), SecureIO(), MasterKey(strconv.Itoa(i)))
		require.NoError(t, err)
		require.NoError(t, host.AddUnicastPubSub("test", func(stream network.Stream) {
			fmt.Printf(", received by %s\n", host.HostIdentity())
		}))
		hosts[i] = host
	}
	time.Sleep(5 * time.Second)

	bootstrapInfo := hosts[0].Info()
	for i := 0; i < n; i++ {
		if i != 0 {
			require.NoError(t, hosts[i].Connect(ctx, bootstrapInfo))
		}
		go hosts[i].Discover(ctx, hosts[i].DHT(), "huski-network")
	}

	for i, host := range hosts {
		neighbors, err := host.Neighbors(ctx)
		require.NoError(t, err)
		require.True(t, len(neighbors) > 0)

		for _, neighbor := range neighbors {
			_, err := host.Unicast(ctx, neighbor, "test", []byte(fmt.Sprintf("msg sent from %s", hosts[i].HostIdentity())))
			require.NoError(
				t,
				err,
			)
		}
	}

	for i := 0; i < n; i++ {
		require.NoError(t, hosts[i].Close())
	}
}

func TestUnicast_ReadReturnedStream(t *testing.T) {
	ctx := context.Background()
	p1, err := NewHost(ctx, Port(30000+1), SecureIO(), MasterKey(strconv.Itoa(1)))
	assert.NoError(t, err)
	p2, err := NewHost(ctx, Port(30000+2), SecureIO(), MasterKey(strconv.Itoa(2)))
	assert.NoError(t, err)

	require.NoError(t, p1.Connect(ctx, p2.Info()))

	var wg sync.WaitGroup
	wg.Add(1)
	require.NoError(t, p2.AddUnicastPubSub("test", func(stream network.Stream) {
		bytes, err := ioutil.ReadAll(stream)
		require.NoError(t, err)
		assert.Equal(t, "ping", string(bytes))

		_, err = stream.Write([]byte("pong"))
		require.NoError(t, err)
		defer assert.NoError(t, stream.CloseWrite())
		wg.Done()
	}))

	stream, err := p1.Unicast(ctx, p2.Info(), "test", []byte("ping"))
	require.NoError(t, err)

	wg.Wait()

	bytes, err := ioutil.ReadAll(stream)
	require.NoError(t, err)
	assert.Equal(t, "pong", string(bytes))

	require.NoError(t, p1.Close())
	require.NoError(t, p2.Close())
}

func Test_NewHost_ExternalOpts_NoMasterKey(t *testing.T) {
	ctx := context.Background()
	opts := []Option{
		Port(30001),
		SecureIO(),
		ExternalHostName("127.0.0.1"),
		ExternalPort(4000),
	}

	host, err := NewHost(ctx, opts...)
	assert.NoError(t, err)
	assert.Equal(t, "127.0.0.1", host.cfg.ExternalHostName)
	assert.Equal(t, 4000, host.cfg.ExternalPort)
	assert.Equal(t, "", host.cfg.MasterKey)

	masterKey := fmt.Sprintf("%s:%d", "127.0.0.1", 4000)
	v1b := cid.V1Builder{Codec: cid.Raw, MhType: multihash.SHA2_256}
	cid, err := v1b.Sum([]byte(masterKey))
	assert.NoError(t, err)
	assert.Equal(t, cid, host.kadKey)

	defer host.Close()
}

func Test_NewHost_ExternalOpts_MasterKey(t *testing.T) {
	ctx := context.Background()
	opts := []Option{
		Port(30001),
		SecureIO(),
		ExternalHostName("0.0.0.0"),
		ExternalPort(4000),
		MasterKey("mk1"),
	}

	host, err := NewHost(ctx, opts...)
	assert.NoError(t, err)
	assert.Equal(t, "0.0.0.0", host.cfg.ExternalHostName)
	assert.Equal(t, 4000, host.cfg.ExternalPort)
	assert.Equal(t, "mk1", host.cfg.MasterKey)

	v1b := cid.V1Builder{Codec: cid.Raw, MhType: multihash.SHA2_256}
	cid, err := v1b.Sum([]byte("mk1"))
	assert.NoError(t, err)
	assert.Equal(t, cid, host.kadKey)

	defer host.Close()
}
