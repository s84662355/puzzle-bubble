package roomclient

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
		return nil, errors.New("empty room grpc addr")
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

func (c *Client) Call(ctx context.Context, playerID string, msgID uint16, body map[string]any) (map[string]any, error) {
	if body == nil {
		body = map[string]any{}
	}
	req, err := structpb.NewStruct(map[string]any{
		"player_id": playerID,
		"msg_id":    float64(msgID),
		"body":      body,
	})
	if err != nil {
		return nil, err
	}
	out := &structpb.Struct{}
	if err := c.conn.Invoke(ctx, "/room.RoomService/HandleGatewayMessage", req, out); err != nil {
		return nil, err
	}
	return out.AsMap(), nil
}
