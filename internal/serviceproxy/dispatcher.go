package serviceproxy

import (
	"context"
	"errors"
	"fmt"

	"game-gateway/internal/config"
	"game-gateway/internal/protocol"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
)

type Dispatcher interface {
	Dispatch(ctx context.Context, playerID string, req protocol.RouteEnvelope) (map[string]any, error)
	Close() error
}

type GRPCDispatcher struct {
	lobbyConn *grpc.ClientConn
	matchConn *grpc.ClientConn
	roomConn  *grpc.ClientConn
}

func NewGRPCDispatcher(cfg config.Config) (*GRPCDispatcher, error) {
	lobbyConn, err := grpc.NewClient(cfg.LobbyGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	matchConn, err := grpc.NewClient(cfg.MatchGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		_ = lobbyConn.Close()
		return nil, err
	}
	roomConn, err := grpc.NewClient(cfg.RoomGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		_ = lobbyConn.Close()
		_ = matchConn.Close()
		return nil, err
	}
	return &GRPCDispatcher{
		lobbyConn: lobbyConn,
		matchConn: matchConn,
		roomConn:  roomConn,
	}, nil
}

func (d *GRPCDispatcher) Close() error {
	var firstErr error
	if d.lobbyConn != nil {
		if err := d.lobbyConn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if d.matchConn != nil {
		if err := d.matchConn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if d.roomConn != nil {
		if err := d.roomConn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (d *GRPCDispatcher) Dispatch(ctx context.Context, playerID string, req protocol.RouteEnvelope) (map[string]any, error) {
	in, err := structpb.NewStruct(map[string]any{
		"player_id": playerID,
		"msg_id":    req.MsgID,
		"body":      req.Body,
	})
	if err != nil {
		return nil, err
	}
	out := &structpb.Struct{}

	switch req.ServiceID {
	case protocol.ServiceLobby:
		err = d.lobbyConn.Invoke(ctx, "/lobby.LobbyService/HandleGatewayMessage", in, out)
	case protocol.ServiceMatch:
		err = d.matchConn.Invoke(ctx, "/match.MatchService/HandleGatewayMessage", in, out)
	case protocol.ServiceRoom:
		err = d.roomConn.Invoke(ctx, "/room.RoomService/HandleGatewayMessage", in, out)
	default:
		return nil, fmt.Errorf("unsupported service_id: %d", req.ServiceID)
	}
	if err != nil {
		return nil, err
	}

	resp := out.AsMap()
	if resp == nil {
		return nil, errors.New("empty service response")
	}
	return resp, nil
}
