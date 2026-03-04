package ui

import (
	"errors"
	"time"

	"game-gateway/internal/gameplay"
)

func (s *Server) ensurePlayerGameStateLocked(roomID, playerID string) {
	if _, ok := s.gameStates[roomID]; !ok {
		s.gameStates[roomID] = make(map[string]*gameplay.PlayerState)
	}
	if _, ok := s.gameStates[roomID][playerID]; ok {
		return
	}
	s.gameStates[roomID][playerID] = gameplay.NewInitialState()
}

func (s *Server) tickRoomStatesLocked(roomID string) {
	now := time.Now().UnixMilli()
	players := s.gameStates[roomID]
	for _, st := range players {
		gameplay.TickState(st, now)
	}
	s.resolveRoomOutcomeLocked(roomID)
}

func cloneState(st *gameplay.PlayerState) map[string]any {
	return gameplay.CloneState(st)
}

func (s *Server) fireLocked(roomID, playerID string, angle float64) error {
	room, ok := s.rooms[roomID]
	if !ok {
		return errors.New("room not found")
	}
	if room.State != "playing" {
		return errors.New("room not playing")
	}
	currentRoomID, ok := s.playerRoom[playerID]
	if !ok || currentRoomID != roomID {
		return errors.New("player not in room")
	}
	s.ensurePlayerGameStateLocked(roomID, playerID)
	return gameplay.Fire(s.gameStates[roomID][playerID], angle)
}

func (s *Server) beginClientShotLocked(roomID, playerID string, angle float64) error {
	room, ok := s.rooms[roomID]
	if !ok {
		return errors.New("room not found")
	}
	if room.State != "playing" {
		return errors.New("room not playing")
	}
	currentRoomID, ok := s.playerRoom[playerID]
	if !ok || currentRoomID != roomID {
		return errors.New("player not in room")
	}
	s.ensurePlayerGameStateLocked(roomID, playerID)
	return gameplay.BeginClientShot(s.gameStates[roomID][playerID], angle, time.Now().UnixMilli())
}

func (s *Server) commitLandingLocked(roomID, playerID string, angle, x, y float64) error {
	room, ok := s.rooms[roomID]
	if !ok {
		return errors.New("room not found")
	}
	if room.State != "playing" {
		return errors.New("room not playing")
	}
	currentRoomID, ok := s.playerRoom[playerID]
	if !ok || currentRoomID != roomID {
		return errors.New("player not in room")
	}
	s.ensurePlayerGameStateLocked(roomID, playerID)
	return gameplay.CommitLanding(s.gameStates[roomID][playerID], angle, x, y)
}

func clampAim(angle float64) float64 {
	return gameplay.ClampAim(angle)
}

func (s *Server) resolveRoomOutcomeLocked(roomID string) {
	room, ok := s.rooms[roomID]
	if !ok || room.State != "playing" {
		return
	}
	members, ok := s.roomMembers[roomID]
	if !ok || len(members) < 2 {
		return
	}
	states, ok := s.gameStates[roomID]
	if !ok {
		return
	}

	alive := make([]string, 0, len(members))
	losers := 0
	for pid := range members {
		st, ok := states[pid]
		if !ok {
			continue
		}
		if st.Status == "lose" {
			losers++
			continue
		}
		alive = append(alive, pid)
	}
	if losers == 0 || len(alive) != 1 {
		return
	}

	winnerID := alive[0]
	room.State = "waiting"
	room.WinnerID = winnerID
	for pid, st := range states {
		st.Moving = nil
		st.PendingShot = false
		if pid == winnerID {
			if st.Status != "lose" {
				st.Status = "win"
			}
			continue
		}
		st.Status = "lose"
	}
}
