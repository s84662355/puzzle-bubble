package gatewaypushclient

import (
	"context"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
)

type Client struct {
	conn *grpc.ClientConn
}

func New(addr string) (*Client, error) {
	if addr == "" {
		return nil, errors.New("empty gateway push grpc addr")
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn}, nil
}

func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *Client) PushToAll(ctx context.Context, cmd uint16, payload map[string]any) (map[string]any, error) {
	req, err := structpb.NewStruct(map[string]any{
		"cmd":     float64(cmd),
		"payload": payload,
	})
	if err != nil {
		return nil, err
	}
	out := &structpb.Struct{}
	if err := c.conn.Invoke(ctx, "/gateway.GatewayPushService/PushToAll", req, out); err != nil {
		return nil, err
	}
	return out.AsMap(), nil
}
