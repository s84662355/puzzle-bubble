package authclient

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
		return nil, errors.New("empty auth grpc addr")
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

func (c *Client) Login(ctx context.Context, username, password string) (map[string]any, error) {
	in, err := structpb.NewStruct(map[string]any{
		"username": username,
		"password": password,
	})
	if err != nil {
		return nil, err
	}
	out := &structpb.Struct{}
	if err := c.conn.Invoke(ctx, "/auth.AuthService/Login", in, out); err != nil {
		return nil, err
	}
	return out.AsMap(), nil
}
