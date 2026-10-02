package ipc

import (
	"context"
	"encoding/json"
	"errors"
)

var ErrStreamsUnavailable = errors.New("stream adapter is not configured")

type BinaryStream interface {
	SendBinary(context.Context, []byte) error
	Close() error
}

type JSONStream interface {
	SendJSON(context.Context, json.RawMessage) error
	Close() error
}

type StreamFactory interface {
	OpenBinary(context.Context, ChannelRef) (BinaryStream, error)
	OpenJSON(context.Context, ChannelRef) (JSONStream, error)
}

type StreamFactoryFuncs struct {
	Binary func(context.Context, ChannelRef) (BinaryStream, error)
	JSON   func(context.Context, ChannelRef) (JSONStream, error)
}

func (f StreamFactoryFuncs) OpenBinary(ctx context.Context, channel ChannelRef) (BinaryStream, error) {
	if f.Binary == nil {
		return nil, ErrStreamsUnavailable
	}
	return f.Binary(ctx, channel)
}

func (f StreamFactoryFuncs) OpenJSON(ctx context.Context, channel ChannelRef) (JSONStream, error) {
	if f.JSON == nil {
		return nil, ErrStreamsUnavailable
	}
	return f.JSON(ctx, channel)
}

type TypedJSONStream[T any] struct {
	stream JSONStream
}

func NewTypedJSONStream[T any](stream JSONStream) *TypedJSONStream[T] {
	return &TypedJSONStream[T]{stream: stream}
}

func OpenTypedJSONStream[T any](ctx context.Context, factory StreamFactory, channel ChannelRef) (*TypedJSONStream[T], error) {
	if factory == nil {
		return nil, ErrStreamsUnavailable
	}
	stream, err := factory.OpenJSON(ctx, channel)
	if err != nil {
		return nil, err
	}
	return NewTypedJSONStream[T](stream), nil
}

func (s *TypedJSONStream[T]) Send(ctx context.Context, value T) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.stream.SendJSON(ctx, data)
}

func (s *TypedJSONStream[T]) Close() error {
	return s.stream.Close()
}
