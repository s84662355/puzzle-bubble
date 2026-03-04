package gameclient

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
		return nil, errors.New("empty game control grpc addr")
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

func (c *Client) CreateSession(ctx context.Context, roomID string, players []string) (map[string]any, error) {
	ps := make([]any, 0, len(players))
	for _, p := range players {
		ps = append(ps, p)
	}
	in, err := structpb.NewStruct(map[string]any{
		"room_id": roomID,
		"players": ps,
	})
	if err != nil {
		return nil, err
	}
	out := &structpb.Struct{}
	if err := c.conn.Invoke(ctx, "/game.GameControlService/CreateSession", in, out); err != nil {
		return nil, err
	}
	return out.AsMap(), nil
}
