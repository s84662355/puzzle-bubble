package userclient

import (
	"context"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
)

type GRPCVerifier struct {
	conn *grpc.ClientConn
}

func NewGRPCVerifier(addr string) (*GRPCVerifier, error) {
	if addr == "" {
		return nil, errors.New("empty user grpc addr")
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &GRPCVerifier{conn: conn}, nil
}

func (v *GRPCVerifier) Close() error {
	if v.conn == nil {
		return nil
	}
	return v.conn.Close()
}

func (v *GRPCVerifier) VerifyToken(ctx context.Context, token string) (playerID string, ok bool, reason string, err error) {
	if token == "" {
		return "", false, "missing_token", nil
	}

	req, err := structpb.NewStruct(map[string]any{
		"token": token,
	})
	if err != nil {
		return "", false, "", err
	}
	resp := &structpb.Struct{}
	if err := v.conn.Invoke(ctx, "/auth.AuthService/ValidateToken", req, resp); err != nil {
		return "", false, "", err
	}

	m := resp.AsMap()
	valid, _ := m["valid"].(bool)
	userID, _ := m["user_id"].(string)
	msg, _ := m["reason"].(string)
	return userID, valid, msg, nil
}

func (v *GRPCVerifier) RefreshToken(ctx context.Context, token string) (ok bool, reason string, err error) {
	if token == "" {
		return false, "missing_token", nil
	}
	req, err := structpb.NewStruct(map[string]any{
		"token": token,
	})
	if err != nil {
		return false, "", err
	}
	resp := &structpb.Struct{}
	if err := v.conn.Invoke(ctx, "/auth.AuthService/RefreshToken", req, resp); err != nil {
		return false, "", err
	}
	m := resp.AsMap()
	valid, _ := m["valid"].(bool)
	msg, _ := m["reason"].(string)
	return valid, msg, nil
}
