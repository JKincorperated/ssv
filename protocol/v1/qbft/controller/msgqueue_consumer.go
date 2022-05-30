package controller

import (
	"context"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/bloxapp/ssv/protocol/v1/message"
	"github.com/bloxapp/ssv/protocol/v1/qbft"
	"github.com/bloxapp/ssv/protocol/v1/qbft/msgqueue"
)

// MessageHandler process the msg. return error if exist
type MessageHandler func(msg *message.SSVMessage) error

func (c *Controller) startQueueConsumer(handler MessageHandler) {
	ctx, cancel := context.WithCancel(c.ctx)
	defer cancel()

	for ctx.Err() == nil {
		err := c.ConsumeQueue(handler, time.Millisecond*10)
		if err != nil {
			c.logger.Warn("could not consume queue", zap.Error(err))
		}
	}
}

// ConsumeQueue consumes messages from the msgqueue.Queue of the controller
// it checks for current state
func (c *Controller) ConsumeQueue(handler MessageHandler, interval time.Duration) error {
	ctx, cancel := context.WithCancel(c.ctx)
	defer cancel()

	for ctx.Err() == nil {
		time.Sleep(interval)

		// no msg's in the queue
		if c.q.Len() == 0 {
			time.Sleep(interval)
			continue // no msg's at all. need to prevent cpu usage in query
		}
		// avoid process messages on fork
		if atomic.LoadUint32(&c.state) == Forking {
			time.Sleep(interval)
			continue
		}

		lastHeight := c.signatureState.getHeight()

		if processed := c.processNoRunningInstance(handler, lastHeight); processed {
			c.logger.Debug("process none running instance is done")
			continue
		}
		if processed := c.processByState(handler); processed {
			c.logger.Debug("process by state is done")
			continue
		}
		if processed := c.processDefault(handler, lastHeight); processed {
			c.logger.Debug("process default is done")
			continue
		}
	}
	c.logger.Warn("queue consumer is closed")
	return nil
}

// processNoRunningInstance pop msg's only if no current instance running
func (c *Controller) processNoRunningInstance(handler MessageHandler, lastHeight message.Height) bool {
	instance := c.getCurrentInstance()
	if instance != nil {
		return false // only pop when no instance running
	}

	iterator := msgqueue.NewIndexIterator().Add(func() string {
		return msgqueue.SignedPostConsensusMsgIndex(c.Identifier, lastHeight)
	}, func() string {
		return msgqueue.DecidedMsgIndex(c.Identifier)
	}, func() string {
		indices := msgqueue.SignedMsgIndex(message.SSVConsensusMsgType, c.Identifier, lastHeight, message.CommitMsgType)
		if len(indices) == 0 {
			return ""
		}
		return indices[0]
	})

	msgs := c.q.PopWithIterator(1, iterator)

	if len(msgs) == 0 || msgs[0] == nil {
		return false // no msg found
	}
	c.logger.Debug("found message in queue when no running instance", zap.String("sig state", c.signatureState.getState().toString()), zap.Int32("height", int32(c.signatureState.getHeight())))
	err := handler(msgs[0])
	if err != nil {
		c.logger.Warn("could not handle msg", zap.Error(err))
	}
	return true // msg processed
}

// processByState if an instance is running -> get the state and get the relevant messages
func (c *Controller) processByState(handler MessageHandler) bool {
	if c.getCurrentInstance() == nil {
		return false
	}

	var msg *message.SSVMessage
	currentState := c.getCurrentInstance().State()
	msg = c.getNextMsgForState(currentState)
	if msg == nil {
		return false // no msg found
	}
	c.logger.Debug("queue found message for state",
		zap.Int32("stage", currentState.Stage.Load()),
		zap.Int32("seq", int32(currentState.GetHeight())),
		zap.Int32("round", int32(currentState.GetRound())),
	)

	err := handler(msg)
	if err != nil {
		c.logger.Warn("could not handle msg", zap.Error(err))
	}
	return true // msg processed
}

// processDefault this phase is to allow late commit and decided msg's
// we allow late commit and decided up to 1 height back. (only to support pre fork. after fork no need to support previews height)
func (c *Controller) processDefault(handler MessageHandler, lastHeight message.Height) bool {
	iterator := msgqueue.NewIndexIterator().
		Add(func() string {
			indices := msgqueue.SignedMsgIndex(message.SSVConsensusMsgType, c.Identifier, lastHeight-1, message.CommitMsgType)
			if len(indices) == 0 {
				return ""
			}
			return indices[0]
		}).Add(func() string {
		indices := msgqueue.SignedMsgIndex(message.SSVDecidedMsgType, c.Identifier, lastHeight-1, message.CommitMsgType)
		if len(indices) == 0 {
			return ""
		}
		return indices[0]
	})
	msgs := c.q.PopWithIterator(1, iterator)

	if len(msgs) > 0 {
		err := handler(msgs[0])
		if err != nil {
			c.logger.Warn("could not handle msg", zap.Error(err))
		}
		return true
	}
	return false
}

// getNextMsgForState return msgs depended on the current instance stage
func (c *Controller) getNextMsgForState(state *qbft.State) *message.SSVMessage {
	height := state.GetHeight()

	iterator := msgqueue.NewIndexIterator().
		Add(func() string {
			var index string
			switch qbft.RoundState(state.Stage.Load()) {
			case qbft.RoundStateNotStarted:
				index = msgqueue.SignedMsgIndex(message.SSVConsensusMsgType, c.Identifier, height, message.ProposalMsgType)[0]
			case qbft.RoundStatePrePrepare:
				index = msgqueue.SignedMsgIndex(message.SSVConsensusMsgType, c.Identifier, height, message.PrepareMsgType)[0] // looking for propose in case is leader
			case qbft.RoundStatePrepare:
				index = msgqueue.SignedMsgIndex(message.SSVConsensusMsgType, c.Identifier, height, message.CommitMsgType)[0]
			case qbft.RoundStateCommit:
				return "" // qbft.RoundStateCommit stage is NEVER set
			case qbft.RoundStateChangeRound:
				index = msgqueue.SignedMsgIndex(message.SSVConsensusMsgType, c.Identifier, height, message.RoundChangeMsgType)[0]
				//case qbft.RoundStateDecided: needs to pop decided msgs in all cases not only by state
			}
			return index
		}).Add(func() string {
		return msgqueue.DecidedMsgIndex(c.Identifier)
	}).Add(func() string {
		indices := msgqueue.SignedMsgIndex(message.SSVConsensusMsgType, c.Identifier, height, message.RoundChangeMsgType)
		if len(indices) == 0 {
			return ""
		}
		return indices[0]
	})
	msgs := c.q.PopWithIterator(1, iterator)
	if len(msgs) > 0 {
		return msgs[0]
	}

	return nil
}

// processOnFork this phase is to allow process remaining decided messages that arrived late to the msg queue
func (c *Controller) processAllDecided(handler MessageHandler) {
	idx := msgqueue.DecidedMsgIndex(c.Identifier)
	msgs := c.q.Pop(1, idx)
	for len(msgs) > 0 {
		err := handler(msgs[0])
		if err != nil {
			c.logger.Warn("could not handle msg", zap.Error(err))
		}
		msgs = c.q.Pop(1, idx)
	}
}
