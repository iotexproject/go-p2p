package p2p

import (
	"context"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/time/rate"
	"google.golang.org/protobuf/proto"

	"github.com/iotexproject/go-pkgs/cache/lru"
	"github.com/iotexproject/iotex-core/v2/action"
	goproto "github.com/iotexproject/iotex-proto/golang"
	"github.com/iotexproject/iotex-proto/golang/iotexrpc"
	"github.com/iotexproject/iotex-proto/golang/iotextypes"
	"github.com/iotexproject/iotex-proto/golang/testingpb"
)

type formatAndSenderValidator struct {
	deser       *action.Deserializer
	sender      *lru.Cache
	rate, burst int
}

func newFormatAndSenderValidator(id uint32, size, rate, burst int) *formatAndSenderValidator {
	return &formatAndSenderValidator{
		deser:  (&action.Deserializer{}).SetEvmNetworkID(id),
		sender: lru.New(size),
		rate:   rate,
		burst:  burst,
	}
}

// Message format and sender validator function
func (val *formatAndSenderValidator) validate(ctx context.Context, peer peer.ID, msg *pubsub.Message) pubsub.ValidationResult {
	var broadcast iotexrpc.BroadcastMsg
	if err := proto.Unmarshal(msg.Data, &broadcast); err != nil {
		return pubsub.ValidationReject
	}
	m, err := goproto.TypifyRPCMsg(broadcast.MsgType, broadcast.MsgBody)
	if err != nil {
		return pubsub.ValidationReject
	}

	switch act := m.(type) {
	case *iotextypes.Block:
		return pubsub.ValidationAccept
	case *iotexrpc.BlockSync:
		println("============== BlockSync must be unicast")
		return pubsub.ValidationReject
	case *iotextypes.Action:
		selp, err := val.deser.ActionToSealedEnvelope(act)
		if err != nil {
			return pubsub.ValidationReject
		}
		if val.allow(selp.SenderAddress().String()) {
			return pubsub.ValidationAccept
		}
		return pubsub.ValidationReject
	case *iotextypes.Actions:
		return pubsub.ValidationAccept
	case *iotextypes.ActionHash:
		println("============== ActionHash must be unicast")
		return pubsub.ValidationReject
	case *iotexrpc.ActionSync:
		println("============== ActionSync must be unicast")
		return pubsub.ValidationReject
	case *iotextypes.ConsensusMessage:
		return pubsub.ValidationAccept
	case *iotextypes.NodeInfoRequest:
		println("============== NodeInfoRequest must be unicast")
		return pubsub.ValidationReject
	case *iotextypes.NodeInfo:
		return pubsub.ValidationAccept
	case *testingpb.TestPayload:
		println("============== should not receive TestPayload")
		return pubsub.ValidationReject
	default:
		println("============== unknown RPC message type")
		return pubsub.ValidationReject
	}
}

func (val *formatAndSenderValidator) allow(src string) bool {
	var limiter *rate.Limiter
	v, ok := val.sender.Get(src)
	if ok {
		limiter, _ = v.(*rate.Limiter)
	} else {
		limiter = rate.NewLimiter(rate.Limit(val.rate), val.burst)
		val.sender.Add(src, limiter)
	}
	return limiter.Allow()
}
