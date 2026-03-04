package ui

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"game-gateway/internal/authclient"
	"game-gateway/internal/gatewaypushclient"
	"game-gateway/internal/gameclient"
	"game-gateway/internal/gameplay"
	"game-gateway/internal/protocol"
	"game-gateway/internal/roomclient"

	"github.com/gorilla/websocket"
)

type Config struct {
	ListenAddr           string
	AuthGRPCAddr         string
	GameControlAddr      string
	RoomGRPCAddr         string
	GatewayPushGRPCAddr  string
	ShutdownTimeout      time.Duration
}

type RoomItem struct {
	RoomID     string `json:"room_id"`
	Name       string `json:"name"`
	OwnerID    string `json:"owner_id"`
	State      string `json:"state"`
	WinnerID   string `json:"winner_id,omitempty"`
	PlayerCnt  int    `json:"player_cnt"`
	MaxPlayers int    `json:"max_players"`
}

type Server struct {
	cfg      Config
	logger   *slog.Logger
	httpSrv  *http.Server
	listener net.Listener
	loopStop context.CancelFunc
	loopWG   sync.WaitGroup

	mu          sync.Mutex
	rooms       map[string]*RoomItem
	playerRoom  map[string]string
	roomMembers map[string]map[string]struct{}
	subscribers map[string]map[chan string]struct{}
	lobbyWSSubs map[*websocket.Conn]struct{}
	gameStates  map[string]map[string]*gameplay.PlayerState // roomID -> playerID -> state
	wsConns     map[string]map[string]*websocket.Conn       // roomID -> playerID -> conn
	roomNextAdd map[string]int64                            // roomID -> next add-layer timestamp(ms)
	roomGameAddr map[string]string
	roomTickets  map[string]map[string]string

	authCli *authclient.Client
	gameCli *gameclient.Client
	roomCli *roomclient.Client
	pushCli *gatewaypushclient.Client
}

func NewServer(cfg Config, logger *slog.Logger) (*Server, error) {
	authCli, err := authclient.New(cfg.AuthGRPCAddr)
	if err != nil {
		return nil, err
	}
	gameCli, err := gameclient.New(cfg.GameControlAddr)
	if err != nil {
		_ = authCli.Close()
		return nil, err
	}
	roomCli, err := roomclient.New(cfg.RoomGRPCAddr)
	if err != nil {
		_ = authCli.Close()
		_ = gameCli.Close()
		return nil, err
	}
	pushCli, err := gatewaypushclient.New(cfg.GatewayPushGRPCAddr)
	if err != nil {
		_ = authCli.Close()
		_ = gameCli.Close()
		_ = roomCli.Close()
		return nil, err
	}

	s := &Server{
		cfg:         cfg,
		logger:      logger,
		rooms:       make(map[string]*RoomItem),
		playerRoom:  make(map[string]string),
		roomMembers: make(map[string]map[string]struct{}),
		subscribers: make(map[string]map[chan string]struct{}),
		lobbyWSSubs: make(map[*websocket.Conn]struct{}),
		gameStates:  make(map[string]map[string]*gameplay.PlayerState),
		wsConns:     make(map[string]map[string]*websocket.Conn),
		roomNextAdd: make(map[string]int64),
		roomGameAddr: make(map[string]string),
		roomTickets:  make(map[string]map[string]string),
		authCli:      authCli,
		gameCli:      gameCli,
		roomCli:      roomCli,
		pushCli:      pushCli,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/lobby", s.handleLobbyPage)
	mux.HandleFunc("/room", s.handleRoomPage)
	mux.HandleFunc("/game", s.handleGamePage)
	mux.HandleFunc("/assets/app.css", s.handleCSS)
	mux.HandleFunc("/assets/login.js", s.handleLoginJS)
	mux.HandleFunc("/assets/lobby.js", s.handleLobbyJS)
	mux.HandleFunc("/assets/room.js", s.handleRoomJS)
	mux.HandleFunc("/assets/game.js", s.handleGameJS)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/lobby/rooms", s.handleRooms)
	mux.HandleFunc("/api/lobby/join", s.handleJoinRoom)
	mux.HandleFunc("/api/lobby/room", s.handleRoomDetail)
	mux.HandleFunc("/api/lobby/start", s.handleStartGame)
	mux.HandleFunc("/api/lobby/leave", s.handleLeaveRoom)
	mux.HandleFunc("/api/lobby/room/events", s.handleRoomEvents)
	mux.HandleFunc("/ws/lobby", s.handleLobbyWS)
	mux.HandleFunc("/api/lobby/game/state", s.handleGameState)
	mux.HandleFunc("/api/lobby/game/fire", s.handleGameFire)
	mux.HandleFunc("/ws/game", s.handleGameWS)

	s.httpSrv = &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s, nil
}

func (s *Server) Start() error {
	ctx, cancel := context.WithCancel(context.Background())
	s.loopStop = cancel
	s.loopWG.Add(1)
	go s.runFrameLoop(ctx)

	ln, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		cancel()
		s.loopWG.Wait()
		return err
	}
	s.listener = ln
	s.logger.Info("ui server started", "addr", s.cfg.ListenAddr)
	return s.httpSrv.Serve(ln)
}

func (s *Server) Stop(ctx context.Context) error {
	if s.loopStop != nil {
		s.loopStop()
		s.loopWG.Wait()
	}
	if s.listener != nil {
		_ = s.listener.Close()
	}
	if s.authCli != nil {
		_ = s.authCli.Close()
	}
	if s.gameCli != nil {
		_ = s.gameCli.Close()
	}
	if s.roomCli != nil {
		_ = s.roomCli.Close()
	}
	if s.pushCli != nil {
		_ = s.pushCli.Close()
	}
	return s.httpSrv.Shutdown(ctx)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.serveFile(w, "web/login.html", "text/html; charset=utf-8")
}

func (s *Server) handleLobbyPage(w http.ResponseWriter, _ *http.Request) {
	s.serveFile(w, "web/lobby.html", "text/html; charset=utf-8")
}

func (s *Server) handleRoomPage(w http.ResponseWriter, _ *http.Request) {
	s.serveFile(w, "web/room.html", "text/html; charset=utf-8")
}

func (s *Server) handleGamePage(w http.ResponseWriter, _ *http.Request) {
	s.serveFile(w, "web/game.html", "text/html; charset=utf-8")
}

func (s *Server) handleCSS(w http.ResponseWriter, _ *http.Request) {
	s.serveFile(w, "web/assets/app.css", "text/css; charset=utf-8")
}

func (s *Server) handleLoginJS(w http.ResponseWriter, _ *http.Request) {
	s.serveFile(w, "web/assets/login.js", "application/javascript; charset=utf-8")
}

func (s *Server) handleLobbyJS(w http.ResponseWriter, _ *http.Request) {
	s.serveFile(w, "web/assets/lobby.js", "application/javascript; charset=utf-8")
}

func (s *Server) handleRoomJS(w http.ResponseWriter, _ *http.Request) {
	s.serveFile(w, "web/assets/room.js", "application/javascript; charset=utf-8")
}

func (s *Server) handleGameJS(w http.ResponseWriter, _ *http.Request) {
	s.serveFile(w, "web/assets/game.js", "application/javascript; charset=utf-8")
}

func (s *Server) serveFile(w http.ResponseWriter, rel string, contentType string) {
	abs := filepath.Join(".", rel)
	data, err := os.ReadFile(abs)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(data)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	out, err := s.authCli.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		http.Error(w, "auth service unavailable", http.StatusBadGateway)
		return
	}
	ok, _ := out["ok"].(bool)
	if !ok {
		http.Error(w, "login failed", http.StatusUnauthorized)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token":      out["token"],
		"player_id":  out["player_id"],
		"expires_in": out["expires_in"],
	})
}

func (s *Server) handleRooms(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listRooms(w, r)
	case http.MethodPost:
		s.createRoom(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) listRooms(w http.ResponseWriter, r *http.Request) {
	playerID := strings.TrimSpace(r.Header.Get("X-Player-Id"))
	if playerID == "" {
		playerID = "lobby-viewer"
	}
	out, err := s.roomCli.Call(r.Context(), playerID, protocol.RoomMsgListRooms, map[string]any{})
	if err != nil {
		http.Error(w, "room service unavailable", http.StatusBadGateway)
		return
	}
	items := make([]*RoomItem, 0)
	if body, ok := out["body"].(map[string]any); ok {
		if rooms, ok := body["rooms"].([]any); ok {
			items = make([]*RoomItem, 0, len(rooms))
			for _, rv := range rooms {
				rm, _ := rv.(map[string]any)
				if it := roomItemFromRemote(rm); it != nil {
					items = append(items, it)
				}
			}
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":  0,
		"rooms": items,
	})
}

func (s *Server) createRoom(w http.ResponseWriter, r *http.Request) {
	playerID := strings.TrimSpace(r.Header.Get("X-Player-Id"))
	if playerID == "" {
		http.Error(w, "missing X-Player-Id", http.StatusUnauthorized)
		return
	}

	var req struct {
		Name       string `json:"name"`
		MaxPlayers int    `json:"max_players"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		req.Name = "Default Room"
	}
	if req.MaxPlayers < 2 {
		req.MaxPlayers = 2
	}
	if req.MaxPlayers > 8 {
		req.MaxPlayers = 8
	}

	out, err := s.roomCli.Call(r.Context(), playerID, protocol.RoomMsgCreateRoom, map[string]any{
		"name":     req.Name,
		"capacity": req.MaxPlayers,
	})
	if err != nil {
		http.Error(w, "room service unavailable", http.StatusBadGateway)
		return
	}
	code, _ := out["code"].(float64)
	if int(code) != 0 {
		http.Error(w, "create room failed", http.StatusConflict)
		return
	}
	body, _ := out["body"].(map[string]any)
	rm, _ := body["room"].(map[string]any)
	room := roomItemFromRemote(rm)
	if room == nil {
		http.Error(w, "invalid room response", http.StatusBadGateway)
		return
	}
	s.syncRoomCacheFromRemote(room, rm)
	s.notifyLobbyRoomsUpdated(playerID, room.RoomID)

	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":   0,
		"room":   room,
		"joined": true,
		"msg":    "created_and_joined",
	})
	s.broadcastRoomEvent(room.RoomID, map[string]any{
		"type":      "room_updated",
		"room_id":   room.RoomID,
		"player_id": playerID,
	})
}

func (s *Server) handleJoinRoom(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	playerID := strings.TrimSpace(r.Header.Get("X-Player-Id"))
	if playerID == "" {
		http.Error(w, "missing X-Player-Id", http.StatusUnauthorized)
		return
	}
	var req struct {
		RoomID string `json:"room_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.RoomID == "" {
		http.Error(w, "missing room_id", http.StatusBadRequest)
		return
	}

	out, err := s.roomCli.Call(r.Context(), playerID, protocol.RoomMsgJoinRoom, map[string]any{
		"room_id": req.RoomID,
	})
	if err != nil {
		http.Error(w, "room service unavailable", http.StatusBadGateway)
		return
	}
	code, _ := out["code"].(float64)
	if int(code) != 0 {
		http.Error(w, "join room failed", http.StatusConflict)
		return
	}
	body, _ := out["body"].(map[string]any)
	rm, _ := body["room"].(map[string]any)
	room := roomItemFromRemote(rm)
	if room == nil {
		http.Error(w, "invalid room response", http.StatusBadGateway)
		return
	}
	s.syncRoomCacheFromRemote(room, rm)
	s.notifyLobbyRoomsUpdated(playerID, room.RoomID)

	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":   0,
		"room":   room,
		"joined": true,
		"msg":    "joined",
	})
	s.broadcastRoomEvent(room.RoomID, map[string]any{
		"type":      "room_updated",
		"room_id":   room.RoomID,
		"player_id": playerID,
	})
}

func (s *Server) handleRoomDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	roomID := strings.TrimSpace(r.URL.Query().Get("room_id"))
	playerID := strings.TrimSpace(r.Header.Get("X-Player-Id"))
	if roomID == "" {
		http.Error(w, "missing room_id", http.StatusBadRequest)
		return
	}

	// Use room service as source of truth to avoid stale UI cache.
	out, err := s.roomCli.Call(r.Context(), playerID, protocol.RoomMsgListRooms, map[string]any{})
	if err != nil {
		http.Error(w, "room service unavailable", http.StatusBadGateway)
		return
	}
	body, _ := out["body"].(map[string]any)
	rooms, _ := body["rooms"].([]any)
	var rm map[string]any
	for _, rv := range rooms {
		cand, _ := rv.(map[string]any)
		if rid, _ := cand["room_id"].(string); rid == roomID {
			rm = cand
			break
		}
	}
	if rm == nil {
		http.Error(w, "room not found", http.StatusNotFound)
		return
	}

	room := roomItemFromRemote(rm)
	if room == nil {
		http.Error(w, "invalid room response", http.StatusBadGateway)
		return
	}
	members := make([]string, 0)
	if ps, ok := rm["players"].([]any); ok {
		members = make([]string, 0, len(ps))
		for _, pv := range ps {
			pm, _ := pv.(map[string]any)
			pid, _ := pm["player_id"].(string)
			if pid != "" {
				members = append(members, pid)
			}
		}
	}
	if playerID != "" {
		inRoom := false
		for _, pid := range members {
			if pid == playerID {
				inRoom = true
				break
			}
		}
		if !inRoom {
			http.Error(w, "player not in room", http.StatusForbidden)
			return
		}
	}
	s.syncRoomCacheFromRemote(room, rm)
	s.mu.Lock()
	gameAddr := s.roomGameAddr[roomID]
	gameTicket := ""
	if ts := s.roomTickets[roomID]; ts != nil {
		gameTicket = ts[playerID]
	}
	s.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":     0,
		"room":     room,
		"members":  members,
		"is_owner": playerID != "" && playerID == room.OwnerID,
		"game_addr": gameAddr,
		"game_ticket": gameTicket,
	})
}

func (s *Server) handleStartGame(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	playerID := strings.TrimSpace(r.Header.Get("X-Player-Id"))
	if playerID == "" {
		http.Error(w, "missing X-Player-Id", http.StatusUnauthorized)
		return
	}
	var req struct {
		RoomID string `json:"room_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.RoomID == "" {
		http.Error(w, "missing room_id", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	room, ok := s.rooms[req.RoomID]
	if !ok {
		s.mu.Unlock()
		http.Error(w, "room not found", http.StatusNotFound)
		return
	}
	if room.OwnerID != playerID {
		s.mu.Unlock()
		http.Error(w, "only owner can start", http.StatusForbidden)
		return
	}
	if room.State == "playing" {
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"room": room,
			"msg":  "already_started",
		})
		return
	}
	memberCount := 0
	if members, ok := s.roomMembers[req.RoomID]; ok {
		memberCount = len(members)
	}
	if memberCount < 2 {
		s.mu.Unlock()
		http.Error(w, "need at least 2 players", http.StatusConflict)
		return
	}
	members := s.roomMembers[req.RoomID]
	players := make([]string, 0, len(members))
	for pid := range members {
		players = append(players, pid)
	}
	s.mu.Unlock()

	sessionOut, err := s.gameCli.CreateSession(r.Context(), req.RoomID, players)
	if err != nil {
		http.Error(w, "game service unavailable", http.StatusBadGateway)
		return
	}
	startOut, err := s.roomCli.Call(r.Context(), playerID, protocol.RoomMsgStartGame, map[string]any{
		"room_id": req.RoomID,
	})
	if err != nil {
		http.Error(w, "room service unavailable", http.StatusBadGateway)
		return
	}
	if code, _ := startOut["code"].(float64); int(code) != 0 {
		http.Error(w, "start room failed", http.StatusConflict)
		return
	}

	s.mu.Lock()
	room, ok = s.rooms[req.RoomID]
	if !ok {
		s.mu.Unlock()
		http.Error(w, "room not found", http.StatusNotFound)
		return
	}
	if room.State == "playing" {
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"room": room,
			"msg":  "already_started",
		})
		return
	}
	if members, ok = s.roomMembers[req.RoomID]; !ok || len(members) < 2 {
		s.mu.Unlock()
		http.Error(w, "room members changed", http.StatusConflict)
		return
	}

	room.State = "playing"
	room.WinnerID = ""
	s.gameStates[req.RoomID] = make(map[string]*gameplay.PlayerState, len(members))
	for pid := range members {
		s.gameStates[req.RoomID][pid] = gameplay.NewInitialState()
	}
	s.roomNextAdd[req.RoomID] = time.Now().UnixMilli() + 10_000
	var gameAddr, playerTicket string
	if ga, ok := sessionOut["game_addr"].(string); ok && ga != "" {
		s.roomGameAddr[req.RoomID] = ga
		gameAddr = ga
	}
	allTicketsReady := false
	if tm, ok := sessionOut["tickets"].(map[string]any); ok {
		playerTickets := make(map[string]string, len(tm))
		for k, v := range tm {
			if sv, ok := v.(string); ok {
				playerTickets[k] = sv
			}
		}
		s.roomTickets[req.RoomID] = playerTickets
		playerTicket = playerTickets[playerID]
		allTicketsReady = true
		for pid := range members {
			if strings.TrimSpace(playerTickets[pid]) == "" {
				allTicketsReady = false
				break
			}
		}
	}
	if gameAddr == "" || playerTicket == "" || !allTicketsReady {
		room.State = "waiting"
		delete(s.gameStates, req.RoomID)
		delete(s.roomNextAdd, req.RoomID)
		delete(s.roomGameAddr, req.RoomID)
		delete(s.roomTickets, req.RoomID)
		s.mu.Unlock()
		http.Error(w, "invalid game session response", http.StatusBadGateway)
		return
	}
	s.mu.Unlock()
	resp := map[string]any{
		"code": 0,
		"room": room,
		"msg":  "started",
	}
	resp["game_addr"] = gameAddr
	resp["game_ticket"] = playerTicket
	_ = json.NewEncoder(w).Encode(resp)
	s.broadcastRoomEvent(req.RoomID, map[string]any{
		"type":    "game_started",
		"room_id": req.RoomID,
		"by":      playerID,
		"ts":      time.Now().UnixMilli(),
	})
}

func (s *Server) handleLeaveRoom(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	playerID := strings.TrimSpace(r.Header.Get("X-Player-Id"))
	var req struct {
		RoomID   string `json:"room_id"`
		PlayerID string `json:"player_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if playerID == "" {
		playerID = strings.TrimSpace(req.PlayerID)
	}
	roomID := strings.TrimSpace(req.RoomID)
	if playerID == "" {
		http.Error(w, "missing player_id", http.StatusBadRequest)
		return
	}
	// Keep Room service as the source of truth for room membership/lifecycle.
	_, _ = s.roomCli.Call(r.Context(), playerID, protocol.RoomMsgLeaveRoom, map[string]any{})

	var (
		remainingPlayerIDs []string
		shouldStopGame     bool
		announceWinner     bool
		winnerPlayerID     string
		roomDeleted        bool
		removed            bool
		closeConn          *websocket.Conn
	)

	s.mu.Lock()
	currentRoomID, inRoom := s.playerRoom[playerID]
	if inRoom {
		if roomID == "" {
			roomID = currentRoomID
		}
		if currentRoomID != roomID {
			s.mu.Unlock()
			http.Error(w, "player not in room", http.StatusForbidden)
			return
		}
		if room, ok := s.rooms[roomID]; ok && room.State == "playing" {
			removed = true
			s.removePlayerFromRoomLocked(playerID, roomID)
			if rm, exists := s.rooms[roomID]; exists {
				memberCnt := len(s.roomMembers[roomID])
				if memberCnt == 1 {
					rm.State = "waiting"
					shouldStopGame = true
					for pid := range s.roomMembers[roomID] {
						remainingPlayerIDs = append(remainingPlayerIDs, pid)
						winnerPlayerID = pid
					}
					if gs, ok := s.gameStates[roomID]; ok {
						if st, ok := gs[winnerPlayerID]; ok && st.Status != "lose" {
							announceWinner = true
						}
					}
					if announceWinner {
						rm.WinnerID = winnerPlayerID
					} else {
						rm.WinnerID = ""
					}
					delete(s.gameStates, roomID)
					delete(s.roomNextAdd, roomID)
					delete(s.roomGameAddr, roomID)
					delete(s.roomTickets, roomID)
				}
			} else {
				roomDeleted = true
				delete(s.roomNextAdd, roomID)
				delete(s.roomGameAddr, roomID)
				delete(s.roomTickets, roomID)
			}
		} else {
			removed = true
			s.removePlayerFromRoomLocked(playerID, roomID)
			if _, exists := s.rooms[roomID]; !exists {
				roomDeleted = true
				delete(s.roomNextAdd, roomID)
				delete(s.roomGameAddr, roomID)
				delete(s.roomTickets, roomID)
			}
		}
		if conns, ok := s.wsConns[roomID]; ok {
			closeConn = conns[playerID]
		}
	}
	s.mu.Unlock()

	if closeConn != nil {
		_ = closeConn.Close()
	}
	if removed {
		s.broadcastRoomEvent(roomID, map[string]any{
			"type":    "room_updated",
			"room_id": roomID,
			"ts":      time.Now().UnixMilli(),
		})
	}
	if shouldStopGame {
		if announceWinner {
			s.broadcastRoomEvent(roomID, map[string]any{
				"type":      "game_over",
				"room_id":   roomID,
				"reason":    "last_player",
				"winner_id": winnerPlayerID,
				"ts":        time.Now().UnixMilli(),
			})
			s.pushGameOverWS(roomID, remainingPlayerIDs, winnerPlayerID)
		} else {
			s.broadcastRoomEvent(roomID, map[string]any{
				"type":    "game_stopped",
				"room_id": roomID,
				"reason":  "player_left",
				"ts":      time.Now().UnixMilli(),
			})
			s.pushGameStoppedWS(roomID, remainingPlayerIDs)
		}
	}
	if roomDeleted && roomID != "" {
		s.broadcastRoomEvent(roomID, map[string]any{
			"type":    "room_deleted",
			"room_id": roomID,
			"ts":      time.Now().UnixMilli(),
		})
	}
	if (removed || roomDeleted) && roomID != "" {
		s.notifyLobbyRoomsUpdated(playerID, roomID)
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":         0,
		"next_page":    "/lobby",
		"room_deleted": roomDeleted,
		"game_stopped": shouldStopGame,
		"winner_id":    winnerPlayerID,
	})
}

func (s *Server) handleRoomEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	roomID := strings.TrimSpace(r.URL.Query().Get("room_id"))
	playerID := strings.TrimSpace(r.URL.Query().Get("player_id"))
	if roomID == "" || playerID == "" {
		http.Error(w, "missing room_id or player_id", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	currentRoomID, inRoom := s.playerRoom[playerID]
	if !inRoom || currentRoomID != roomID {
		s.mu.Unlock()
		http.Error(w, "player not in room", http.StatusForbidden)
		return
	}
	ch := make(chan string, 16)
	if _, ok := s.subscribers[roomID]; !ok {
		s.subscribers[roomID] = make(map[chan string]struct{})
	}
	s.subscribers[roomID][ch] = struct{}{}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		if subs, ok := s.subscribers[roomID]; ok {
			delete(subs, ch)
			if len(subs) == 0 {
				delete(s.subscribers, roomID)
			}
		}
		s.mu.Unlock()
		close(ch)
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	_, _ = w.Write([]byte("event: ping\ndata: {}\n\n"))
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			_, _ = w.Write([]byte("event: room\ndata: " + msg + "\n\n"))
			flusher.Flush()
		case <-ticker.C:
			_, _ = w.Write([]byte("event: ping\ndata: {}\n\n"))
			flusher.Flush()
		}
	}
}

func (s *Server) broadcastRoomEvent(roomID string, evt map[string]any) {
	data, err := json.Marshal(evt)
	if err != nil {
		return
	}
	payload := string(data)

	s.mu.Lock()
	defer s.mu.Unlock()
	subs, ok := s.subscribers[roomID]
	if !ok {
		return
	}
	for ch := range subs {
		select {
		case ch <- payload:
		default:
			// Drop if receiver is slow.
		}
	}
}

func (s *Server) handleLobbyWS(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.lobbyWSSubs[conn] = struct{}{}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.lobbyWSSubs, conn)
		s.mu.Unlock()
		_ = conn.Close()
	}()

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (s *Server) broadcastLobbyEvent(evt map[string]any) {
	data, err := json.Marshal(evt)
	if err != nil {
		return
	}
	s.mu.Lock()
	conns := make([]*websocket.Conn, 0, len(s.lobbyWSSubs))
	for c := range s.lobbyWSSubs {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
			s.mu.Lock()
			delete(s.lobbyWSSubs, c)
			s.mu.Unlock()
			_ = c.Close()
		}
	}
}

func (s *Server) pushGameStoppedWS(roomID string, playerIDs []string) {
	msg := map[string]any{
		"type":    "game_stopped",
		"room_id": roomID,
		"reason":  "player_left",
		"next":    "/room",
	}
	s.mu.Lock()
	targets := make([]*websocket.Conn, 0, len(playerIDs))
	for _, pid := range playerIDs {
		if conns, ok := s.wsConns[roomID]; ok {
			if c := conns[pid]; c != nil {
				targets = append(targets, c)
			}
		}
	}
	s.mu.Unlock()
	for _, c := range targets {
		_ = c.WriteJSON(msg)
	}
}

func (s *Server) pushGameOverWS(roomID string, playerIDs []string, winnerID string) {
	msg := map[string]any{
		"type":      "game_over",
		"room_id":   roomID,
		"reason":    "last_player",
		"winner_id": winnerID,
		"result":    "win",
		"next":      "/room",
	}
	s.mu.Lock()
	targets := make([]*websocket.Conn, 0, len(playerIDs))
	for _, pid := range playerIDs {
		if conns, ok := s.wsConns[roomID]; ok {
			if c := conns[pid]; c != nil {
				targets = append(targets, c)
			}
		}
	}
	s.mu.Unlock()
	for _, c := range targets {
		_ = c.WriteJSON(msg)
	}
}

func (s *Server) runFrameLoop(ctx context.Context) {
	defer s.loopWG.Done()
	ticker := time.NewTicker(33 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			s.stepRoomsLocked()
			s.mu.Unlock()
		}
	}
}

func (s *Server) stepRoomsLocked() {
	now := time.Now().UnixMilli()
	for roomID, room := range s.rooms {
		if room == nil {
			delete(s.roomNextAdd, roomID)
			continue
		}
		if room.State != "playing" {
			delete(s.roomNextAdd, roomID)
			continue
		}
		s.tickRoomStatesLocked(roomID)
		nextAt, ok := s.roomNextAdd[roomID]
		if !ok || nextAt <= 0 {
			s.roomNextAdd[roomID] = now + 10_000
			continue
		}
		if now >= nextAt && !s.canAddLayerLocked(roomID) {
			// Wait until all players are idle to avoid add-layer and shot sync conflicts.
			s.roomNextAdd[roomID] = nextAt
			continue
		}
		for now >= nextAt {
			if !s.canAddLayerLocked(roomID) {
				break
			}
			if states, ok := s.gameStates[roomID]; ok {
				for _, st := range states {
					gameplay.AddTopLayer(st)
				}
			}
			nextAt += 10_000
		}
		s.roomNextAdd[roomID] = nextAt
	}
}

func (s *Server) canAddLayerLocked(roomID string) bool {
	states, ok := s.gameStates[roomID]
	if !ok || len(states) == 0 {
		return false
	}
	for _, st := range states {
		if st == nil {
			continue
		}
		if st.Status != "playing" {
			return false
		}
		if st.Moving != nil || st.PendingShot {
			return false
		}
	}
	return true
}

func strconvNow() string {
	return strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
}

func (s *Server) removePlayerFromRoomLocked(playerID, roomID string) {
	room, ok := s.rooms[roomID]
	if !ok {
		delete(s.playerRoom, playerID)
		return
	}

	if members, exists := s.roomMembers[roomID]; exists {
		if _, inRoom := members[playerID]; inRoom {
			delete(members, playerID)
			if room.PlayerCnt > 0 {
				room.PlayerCnt--
			}
		}

		if len(members) == 0 {
			delete(s.roomMembers, roomID)
			delete(s.rooms, roomID)
			delete(s.gameStates, roomID)
			delete(s.roomNextAdd, roomID)
			delete(s.roomGameAddr, roomID)
			delete(s.roomTickets, roomID)
		} else {
			if room.OwnerID == playerID {
				for nextOwner := range members {
					room.OwnerID = nextOwner
					break
				}
			}
		}
	}

	delete(s.playerRoom, playerID)
	if gs, ok := s.gameStates[roomID]; ok {
		delete(gs, playerID)
		if len(gs) == 0 {
			delete(s.gameStates, roomID)
		}
	}
}

func (s *Server) handleGameState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	playerID := strings.TrimSpace(r.Header.Get("X-Player-Id"))
	roomID := strings.TrimSpace(r.URL.Query().Get("room_id"))
	if playerID == "" || roomID == "" {
		http.Error(w, "missing player_id or room_id", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	currentRoomID, ok := s.playerRoom[playerID]
	if !ok || currentRoomID != roomID {
		http.Error(w, "player not in room", http.StatusForbidden)
		return
	}
	if _, ok := s.rooms[roomID]; !ok {
		http.Error(w, "room not found", http.StatusNotFound)
		return
	}

	s.ensurePlayerGameStateLocked(roomID, playerID)

	self := cloneState(s.gameStates[roomID][playerID])
	others := make([]map[string]any, 0)
	for pid, st := range s.gameStates[roomID] {
		if pid == playerID {
			continue
		}
		others = append(others, map[string]any{
			"player_id": pid,
			"state":     cloneState(st),
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":   0,
		"room":   s.rooms[roomID],
		"self":   self,
		"others": others,
	})
}

func (s *Server) handleGameFire(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	playerID := strings.TrimSpace(r.Header.Get("X-Player-Id"))
	if playerID == "" {
		http.Error(w, "missing X-Player-Id", http.StatusUnauthorized)
		return
	}
	var req struct {
		RoomID string  `json:"room_id"`
		Angle  float64 `json:"angle"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.RoomID == "" {
		http.Error(w, "missing room_id", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fireLocked(req.RoomID, playerID, req.Angle); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0})
}

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (s *Server) handleGameWS(w http.ResponseWriter, r *http.Request) {
	playerID := strings.TrimSpace(r.URL.Query().Get("player_id"))
	roomID := strings.TrimSpace(r.URL.Query().Get("room_id"))
	if playerID == "" || roomID == "" {
		http.Error(w, "missing player_id or room_id", http.StatusBadRequest)
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	s.mu.Lock()
	currentRoomID, ok := s.playerRoom[playerID]
	if !ok || currentRoomID != roomID {
		s.mu.Unlock()
		_ = conn.WriteJSON(map[string]any{"type": "error", "msg": "player not in room"})
		_ = conn.Close()
		return
	}
	if _, ok := s.wsConns[roomID]; !ok {
		s.wsConns[roomID] = make(map[string]*websocket.Conn)
	}
	if old := s.wsConns[roomID][playerID]; old != nil {
		_ = old.Close()
	}
	s.wsConns[roomID][playerID] = conn
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		if m, ok := s.wsConns[roomID]; ok {
			if cur := m[playerID]; cur == conn {
				delete(m, playerID)
			}
			if len(m) == 0 {
				delete(s.wsConns, roomID)
			}
		}
		s.mu.Unlock()
		_ = conn.Close()
	}()

	done := make(chan struct{})
	go s.readGameWS(conn, roomID, playerID, done)

	ticker := time.NewTicker(70 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			payload, ok := s.buildWSState(roomID, playerID)
			if !ok {
				_ = conn.WriteJSON(map[string]any{"type": "error", "msg": "room closed"})
				return
			}
			if err := conn.WriteJSON(payload); err != nil {
				return
			}
		}
	}
}

func (s *Server) readGameWS(conn *websocket.Conn, roomID, playerID string, done chan struct{}) {
	defer close(done)
	for {
		var req map[string]any
		if err := conn.ReadJSON(&req); err != nil {
			return
		}
		typ, _ := req["type"].(string)
		switch typ {
		case "shot":
			angle, _ := req["angle"].(float64)
			s.mu.Lock()
			err := s.beginClientShotLocked(roomID, playerID, angle)
			s.mu.Unlock()
			if err != nil {
				_ = conn.WriteJSON(map[string]any{"type": "error", "msg": err.Error()})
			}
		case "fire":
			angle, _ := req["angle"].(float64)
			s.mu.Lock()
			err := s.fireLocked(roomID, playerID, angle)
			s.mu.Unlock()
			if err != nil {
				_ = conn.WriteJSON(map[string]any{"type": "error", "msg": err.Error()})
			}
		case "land":
			angle, _ := req["angle"].(float64)
			x, _ := req["x"].(float64)
			y, _ := req["y"].(float64)
			s.mu.Lock()
			err := s.commitLandingLocked(roomID, playerID, angle, x, y)
			s.mu.Unlock()
			if err != nil {
				_ = conn.WriteJSON(map[string]any{"type": "error", "msg": err.Error()})
			}
		case "aim":
			angle, _ := req["angle"].(float64)
			s.mu.Lock()
			if currentRoomID, ok := s.playerRoom[playerID]; ok && currentRoomID == roomID {
				s.ensurePlayerGameStateLocked(roomID, playerID)
				s.gameStates[roomID][playerID].AimAngle = clampAim(angle)
			}
			s.mu.Unlock()
		}
	}
}

func (s *Server) buildWSState(roomID, playerID string) (map[string]any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	currentRoomID, ok := s.playerRoom[playerID]
	if !ok || currentRoomID != roomID {
		return nil, false
	}
	room, ok := s.rooms[roomID]
	if !ok {
		return nil, false
	}
	s.ensurePlayerGameStateLocked(roomID, playerID)

	self := cloneState(s.gameStates[roomID][playerID])
	others := make([]map[string]any, 0)
	for pid, st := range s.gameStates[roomID] {
		if pid == playerID {
			continue
		}
		others = append(others, map[string]any{
			"player_id": pid,
			"state":     cloneState(st),
		})
	}
	return map[string]any{
		"type":   "state",
		"room":   room,
		"self":   self,
		"others": others,
	}, true
}

func roomItemFromRemote(rm map[string]any) *RoomItem {
	if rm == nil {
		return nil
	}
	roomID, _ := rm["room_id"].(string)
	if roomID == "" {
		return nil
	}
	name, _ := rm["name"].(string)
	ownerID, _ := rm["owner_id"].(string)
	state, _ := rm["state"].(string)
	maxPlayers := int(toFloat64(rm["max_players"]))
	playerCnt := 0
	if ps, ok := rm["players"].([]any); ok {
		playerCnt = len(ps)
	}
	if maxPlayers <= 0 {
		maxPlayers = 2
	}
	return &RoomItem{
		RoomID:     roomID,
		Name:       name,
		OwnerID:    ownerID,
		State:      toUIRoomState(state),
		PlayerCnt:  playerCnt,
		MaxPlayers: maxPlayers,
	}
}

func toUIRoomState(remote string) string {
	switch strings.ToLower(remote) {
	case "playing":
		return "playing"
	case "closed", "settlement":
		return "closed"
	default:
		return "waiting"
	}
}

func toFloat64(v any) float64 {
	if n, ok := v.(float64); ok {
		return n
	}
	return 0
}

func (s *Server) syncRoomCacheFromRemote(room *RoomItem, rm map[string]any) {
	if room == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rooms[room.RoomID] = room
	s.roomMembers[room.RoomID] = make(map[string]struct{})
	if ps, ok := rm["players"].([]any); ok {
		for _, p := range ps {
			pm, _ := p.(map[string]any)
			pid, _ := pm["player_id"].(string)
			if pid == "" {
				continue
			}
			s.roomMembers[room.RoomID][pid] = struct{}{}
			s.playerRoom[pid] = room.RoomID
		}
	}
}

func (s *Server) notifyLobbyRoomsUpdated(byPlayerID, roomID string) {
	evt := map[string]any{
		"type":      "lobby_rooms_updated",
		"room_id":   roomID,
		"player_id": byPlayerID,
		"ts":        time.Now().UnixMilli(),
	}
	s.broadcastLobbyEvent(evt)
	if s.pushCli == nil {
		return
	}
	_, _ = s.pushCli.PushToAll(context.Background(), protocol.CmdServerPush, evt)
}

func ValidateConfig(cfg Config) error {
	if cfg.ListenAddr == "" {
		return errors.New("empty listen addr")
	}
	if cfg.AuthGRPCAddr == "" {
		return errors.New("empty auth grpc addr")
	}
	if cfg.GameControlAddr == "" {
		return errors.New("empty game control grpc addr")
	}
	if cfg.RoomGRPCAddr == "" {
		return errors.New("empty room grpc addr")
	}
	if cfg.GatewayPushGRPCAddr == "" {
		return errors.New("empty gateway push grpc addr")
	}
	return nil
}
