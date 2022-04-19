package topics

import (
	"context"
	"github.com/bloxapp/ssv/utils/logex"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"go.uber.org/zap"
	"sync"
	"sync/atomic"
)

type topicContainer struct {
	topic  *pubsub.Topic
	sub    *pubsub.Subscription
	locker *sync.Mutex
	// count is the number of subscriptions made for this topic
	subsCount int32
}

func newTopicContainer() *topicContainer {
	return &topicContainer{
		locker: &sync.Mutex{},
	}
}

func (tc *topicContainer) Close() error {
	if tc.sub != nil {
		tc.sub.Cancel()
		tc.sub = nil
	}
	if tc.topic != nil {
		if err := tc.topic.Close(); err != nil {
			return err
		}
		tc.topic = nil
	}
	return nil
}

func (tc *topicContainer) incSubCount() int32 {
	return atomic.AddInt32(&tc.subsCount, 1)
}

func (tc *topicContainer) decSubCount() int32 {
	return atomic.AddInt32(&tc.subsCount, -1)
}

func (tc *topicContainer) Publish(ctx context.Context, data []byte) error {
	tc.locker.Lock()
	defer tc.locker.Unlock()

	if tc.topic == nil {
		return ErrTopicNotReady
	}
	err := tc.topic.Publish(ctx, data)
	if err == nil {
		metricsPubsubOutbound.WithLabelValues(getTopicBaseName(tc.topic.String())).Inc()
	}
	logex.GetLogger().Debug("broadcast success", zap.Error(err))
	return err
}
