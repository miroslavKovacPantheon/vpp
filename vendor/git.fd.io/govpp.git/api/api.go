// Copyright (c) 2017 Cisco and/or its affiliates.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at:
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"errors"
	"fmt"
	"time"
)

// MessageType represents the type of a VPP message.
type MessageType int

const (
	// RequestMessage represents a VPP request message
	RequestMessage MessageType = iota
	// ReplyMessage represents a VPP reply message
	ReplyMessage
	// EventMessage represents a VPP notification event message
	EventMessage
	// OtherMessage represents other VPP message (e.g. counters)
	OtherMessage
)

// Message is an interface that is implemented by all VPP Binary API messages generated by the binapigenerator.
type Message interface {
	// GetMessageName returns the original VPP name of the message, as defined in the VPP API.
	GetMessageName() string

	// GetMessageType returns the type of the VPP message.
	GetMessageType() MessageType

	// GetCrcString returns the string with CRC checksum of the message definition (the string represents a hexadecimal number).
	GetCrcString() string
}

// DataType is an interface that is implemented by all VPP Binary API data types by the binapi_generator.
type DataType interface {
	// GetTypeName returns the original VPP name of the data type, as defined in the VPP API.
	GetTypeName() string

	// GetCrcString returns the string with CRC checksum of the data type definition (the string represents a hexadecimal number).
	GetCrcString() string
}

// ChannelProvider provides the communication channel with govpp core.
type ChannelProvider interface {
	// NewAPIChannel returns a new channel for communication with VPP via govpp core.
	// It uses default buffer sizes for the request and reply Go channels.
	NewAPIChannel() (*Channel, error)

	// NewAPIChannel returns a new channel for communication with VPP via govpp core.
	// It allows to specify custom buffer sizes for the request and reply Go channels.
	NewAPIChannelBuffered() (*Channel, error)
}

// MessageDecoder provides functionality for decoding binary data to generated API messages.
type MessageDecoder interface {
	// DecodeMsg decodes binary-encoded data of a message into provided Message structure.
	DecodeMsg(data []byte, msg Message) error
}

// MessageIdentifier provides identification of generated API messages.
type MessageIdentifier interface {
	// GetMessageID returns message identifier of given API message.
	GetMessageID(msg Message) (uint16, error)
}

// Channel is the main communication interface with govpp core. It contains two Go channels, one for sending the requests
// to VPP and one for receiving the replies from it. The user can access the Go channels directly, or use the helper
// methods  provided inside of this package. Do not use the same channel from multiple goroutines concurrently,
// otherwise the responses could mix! Use multiple channels instead.
type Channel struct {
	ReqChan   chan *VppRequest // channel for sending the requests to VPP, closing this channel releases all resources in the ChannelProvider
	ReplyChan chan *VppReply   // channel where VPP replies are delivered to

	NotifSubsChan      chan *NotifSubscribeRequest // channel for sending notification subscribe requests
	NotifSubsReplyChan chan error                  // channel where replies to notification subscribe requests are delivered to

	MsgDecoder    MessageDecoder    // used to decode binary data to generated API messages
	MsgIdentifier MessageIdentifier // used to retrieve message ID of a message

	replyTimeout time.Duration // maximum time that the API waits for a reply from VPP before returning an error, can be set with SetReplyTimeout
	metadata     interface{}   // opaque metadata of the API channel
}

// VppRequest is a request that will be sent to VPP.
type VppRequest struct {
	Message   Message // binary API message to be send to VPP
	Multipart bool    // true if multipart response is expected, false otherwise
}

// VppReply is a reply received from VPP.
type VppReply struct {
	MessageID         uint16 // ID of the message
	Data              []byte // encoded data with the message - MessageDecoder can be used for decoding
	LastReplyReceived bool   // in case of multipart replies, true if the last reply has been already received and this one should be ignored
	Error             error  // in case of error, data is nil and this member contains error description
}

// NotifSubscribeRequest is a request to subscribe for delivery of specific notification messages.
type NotifSubscribeRequest struct {
	Subscription *NotifSubscription // subscription details
	Subscribe    bool               // true if this is a request to subscribe, false if unsubscribe
}

// NotifSubscription represents a subscription for delivery of specific notification messages.
type NotifSubscription struct {
	NotifChan  chan Message   // channel where notification messages will be delivered to
	MsgFactory func() Message // function that returns a new instance of the specific message that is expected as a notification
}

// RequestCtx is a context of a ongoing request (simple one - only one response is expected).
type RequestCtx struct {
	ch *Channel
}

// MultiRequestCtx is a context of a ongoing multipart request (multiple responses are expected).
type MultiRequestCtx struct {
	ch *Channel
}

const defaultReplyTimeout = time.Second * 1 // default timeout for replies from VPP, can be changed with SetReplyTimeout

// NewChannelInternal returns a new channel structure with metadata field filled in with the provided argument.
// Note that this is just a raw channel not yet connected to VPP, it is not intended to be used directly.
// Use ChannelProvider to get an API channel ready for communication with VPP.
func NewChannelInternal(metadata interface{}) *Channel {
	return &Channel{
		replyTimeout: defaultReplyTimeout,
		metadata:     metadata,
	}
}

// Metadata returns the metadata stored within the channel structure by the NewChannelInternal call.
func (ch *Channel) Metadata() interface{} {
	return ch.metadata
}

// SetReplyTimeout sets the timeout for replies from VPP. It represents the maximum time the API waits for a reply
// from VPP before returning an error.
func (ch *Channel) SetReplyTimeout(timeout time.Duration) {
	ch.replyTimeout = timeout
}

// Close closes the API channel and releases all API channel-related resources in the ChannelProvider.
func (ch *Channel) Close() {
	if ch.ReqChan != nil {
		close(ch.ReqChan)
	}
}

// SendRequest asynchronously sends a request to VPP. Returns a request context, that can be used to call ReceiveReply.
// In case of any errors by sending, the error will be delivered to ReplyChan (and returned by ReceiveReply).
func (ch *Channel) SendRequest(msg Message) *RequestCtx {
	ch.ReqChan <- &VppRequest{
		Message: msg,
	}
	return &RequestCtx{ch: ch}
}

// ReceiveReply receives a reply from VPP (blocks until a reply is delivered from VPP, or until an error occurs).
// The reply will be decoded into the msg argument. Error will be returned if the response cannot be received or decoded.
func (req *RequestCtx) ReceiveReply(msg Message) error {
	if req == nil || req.ch == nil {
		return errors.New("invalid request context")
	}

	lastReplyReceived, err := req.ch.receiveReplyInternal(msg)

	if lastReplyReceived {
		err = errors.New("multipart reply recieved while a simple reply expected")
	}
	return err
}

// SendMultiRequest asynchronously sends a multipart request (request to which multiple responses are expected) to VPP.
// Returns a multipart request context, that can be used to call ReceiveReply.
// In case of any errors by sending, the error will be delivered to ReplyChan (and returned by ReceiveReply).
func (ch *Channel) SendMultiRequest(msg Message) *MultiRequestCtx {
	ch.ReqChan <- &VppRequest{
		Message:   msg,
		Multipart: true,
	}
	return &MultiRequestCtx{ch: ch}
}

// ReceiveReply receives a reply from VPP (blocks until a reply is delivered from VPP, or until an error occurs).
// The reply will be decoded into the msg argument. If the last reply has been already consumed, LastReplyReceived is
// set to true. Do not use the message itself if LastReplyReceived is true - it won't be filled with actual data.
// Error will be returned if the response cannot be received or decoded.
func (req *MultiRequestCtx) ReceiveReply(msg Message) (LastReplyReceived bool, err error) {
	if req == nil || req.ch == nil {
		return false, errors.New("invalid request context")
	}

	return req.ch.receiveReplyInternal(msg)
}

// receiveReplyInternal receives a reply from the reply channel into the provided msg structure.
func (ch *Channel) receiveReplyInternal(msg Message) (LastReplyReceived bool, err error) {
	if msg == nil {
		return false, errors.New("nil message passed in")
	}
	select {
	// blocks until a reply comes to ReplyChan or until timeout expires
	case vppReply := <-ch.ReplyChan:
		if vppReply.Error != nil {
			err = vppReply.Error
			return
		}
		if vppReply.LastReplyReceived {
			LastReplyReceived = true
			return
		}
		// message checks
		expMsgID, err := ch.MsgIdentifier.GetMessageID(msg)
		if err != nil {
			err = fmt.Errorf("message %s with CRC %s is not compatible with the VPP we are connected to",
				msg.GetMessageName(), msg.GetCrcString())
			return false, err
		}
		if vppReply.MessageID != expMsgID {
			err = fmt.Errorf("received invalid message ID, expected %d (%s), but got %d (check if multiple goroutines are not sharing single GoVPP channel)",
				expMsgID, msg.GetMessageName(), vppReply.MessageID)
			return false, err
		}
		// decode the message
		err = ch.MsgDecoder.DecodeMsg(vppReply.Data, msg)

	case <-time.After(ch.replyTimeout):
		err = fmt.Errorf("no reply received within the timeout period %s", ch.replyTimeout)
	}
	return
}

// SubscribeNotification subscribes for receiving of the specified notification messages via provided Go channel.
// Note that the caller is responsible for creating the Go channel with preferred buffer size. If the channel's
// buffer is full, the notifications will not be delivered into it.
func (ch *Channel) SubscribeNotification(notifChan chan Message, msgFactory func() Message) (*NotifSubscription, error) {
	subscription := &NotifSubscription{
		NotifChan:  notifChan,
		MsgFactory: msgFactory,
	}
	ch.NotifSubsChan <- &NotifSubscribeRequest{
		Subscription: subscription,
		Subscribe:    true,
	}
	return subscription, <-ch.NotifSubsReplyChan
}

// UnsubscribeNotification unsubscribes from receiving the notifications tied to the provided notification subscription.
func (ch *Channel) UnsubscribeNotification(subscription *NotifSubscription) error {
	ch.NotifSubsChan <- &NotifSubscribeRequest{
		Subscription: subscription,
		Subscribe:    false,
	}
	return <-ch.NotifSubsReplyChan
}

// CheckMessageCompatibility checks whether provided messages are compatible with the version of VPP
// which the library is connected to.
func (ch *Channel) CheckMessageCompatibility(messages ...Message) error {
	for _, msg := range messages {
		_, err := ch.MsgIdentifier.GetMessageID(msg)
		if err != nil {
			return fmt.Errorf("message %s with CRC %s is not compatible with the VPP we are connected to",
				msg.GetMessageName(), msg.GetCrcString())
		}
	}
	return nil
}
