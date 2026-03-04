package game

import (
	"context"
	"time"
)

func RegisterBuiltinHandlers(r *Router) error {
	if err := r.Register("system", "ping", systemPing); err != nil {
		return err
	}
	if err := r.Register("player", "profile", playerProfile); err != nil {
		return err
	}
	return nil
}

func systemPing(_ context.Context, s SessionView, _ Message) (Response, error) {
	return Response{
		Code: 0,
		Msg:  "ok",
		Body: map[string]any{
			"player_id": s.PlayerID,
			"ts":        time.Now().UTC().Unix(),
		},
	}, nil
}

func playerProfile(_ context.Context, s SessionView, _ Message) (Response, error) {
	// Placeholder: replace with player-service query.
	return Response{
		Code: 0,
		Msg:  "ok",
		Body: map[string]any{
			"player_id": s.PlayerID,
			"nickname":  "player_" + s.PlayerID,
			"level":     1,
		},
	}, nil
}
