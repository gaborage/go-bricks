package messaging

import (
	amqp "github.com/rabbitmq/amqp091-go"
)

// pendingPublish is one in-flight publish waiting for its broker confirmation,
// stored under its (generation, delivery tag). messageID is set for a Mandatory
// publish only. returned is written and read only by its generation's dispatcher.
type pendingPublish struct {
	confirm   chan publishConfirm
	messageID string
	returned  *publishReturn
}

// mandatoryKey finds a generation's pending Mandatory publish by message id, the
// only address a basic.return carries.
type mandatoryKey struct {
	generation uint64
	messageID  string
}

// publishConfirm is the broker's answer to one publish: its confirmation, and the
// return that preceded it when the broker could not route it.
type publishConfirm struct {
	amqp.Confirmation
	returned *publishReturn
}

// publishReturn is what the client keeps of a basic.return: the broker's reply and
// the message id it was matched by. The returned body and headers are never kept.
type publishReturn struct {
	replyCode uint16
	replyText string
	messageID string
}

// trackPending registers p under key, and a Mandatory publish's message id in
// the index recordReturn reads.
func (c *AMQPClientImpl) trackPending(key confirmKey, p *pendingPublish) {
	if p.messageID != "" {
		c.pendingMandatory.Store(mandatoryKey{generation: key.generation, messageID: p.messageID}, p)
	}
	c.pendingPublishes.Store(key, p)
}

// untrackPending removes key's pending publish and its index entry, and returns
// it, or nil when another path already removed it. CompareAndDelete keeps the
// entry of a later publish that reused the message id.
func (c *AMQPClientImpl) untrackPending(key confirmKey) *pendingPublish {
	v, ok := c.pendingPublishes.LoadAndDelete(key)
	if !ok {
		return nil
	}
	p, ok := v.(*pendingPublish)
	if !ok {
		return nil
	}
	if p.messageID != "" {
		c.pendingMandatory.CompareAndDelete(mandatoryKey{generation: key.generation, messageID: p.messageID}, p)
	}
	return p
}

// recordReturn attaches ret to generation gen's pending Mandatory publish that
// carries its message id, if one is still waiting. A return that matches none is
// dropped with a DEBUG line naming the broker's reply and the message id; the
// returned body and headers never reach it.
func (c *AMQPClientImpl) recordReturn(gen uint64, ret *amqp.Return) {
	v, ok := c.pendingMandatory.Load(mandatoryKey{generation: gen, messageID: ret.MessageId})
	if !ok {
		withBrokerReply(c.log.Debug(), int(ret.ReplyCode), ret.ReplyText).
			Str("message_id", ret.MessageId).
			Uint64("generation", gen).
			Msg("Dropped a broker return that matches no waiting publish")
		return
	}
	p, ok := v.(*pendingPublish)
	if !ok {
		return
	}
	p.returned = &publishReturn{replyCode: ret.ReplyCode, replyText: ret.ReplyText, messageID: ret.MessageId}
}

// drainReturns records every buffered return without blocking, and answers nil
// once returns is closed.
func (c *AMQPClientImpl) drainReturns(gen uint64, returns <-chan amqp.Return) <-chan amqp.Return {
	for {
		select {
		case ret, ok := <-returns:
			if !ok {
				return nil
			}
			c.recordReturn(gen, &ret)
		default:
			return returns
		}
	}
}
