package mono

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
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

	"game-gateway/internal/gameplay"

	"github.com/gorilla/websocket"
	_ "modernc.org/sqlite"
)

type Config struct {
	ListenAddr       string
	DBPath           string
	TokenTTL         time.Duration
	HeartbeatTimeout time.Duration
	ShutdownTimeout  time.Duration
}

type Room struct {
	ID         string              `json:"room_id"`
	Name       string              `json:"name"`
	OwnerID    string              `json:"owner_id"`
	State      string              `json:"state"`
	WinnerID   string              `json:"winner_id,omitempty"`
	MaxPlayers int                 `json:"max_players"`
	Players    map[string]struct{} `json:"-"`
}

type Server struct {
	cfg    Config
	logger *slog.Logger

	db       *sql.DB
	httpSrv  *http.Server
	listener net.Listener

	mu          sync.Mutex
	rooms       map[string]*Room
	playerRoom  map[string]string
	gameStates  map[string]map[string]*gameplay.PlayerState
	roomConns   map[string]map[string]*websocket.Conn
	lastHB      map[string]int64
	roomTickets map[string]map[string]string
	roomNextAdd map[string]int64

	lobbyMu     sync.Mutex
	lobbyWSSubs map[*websocket.Conn]struct{}
}

func New(cfg Config, logger *slog.Logger) (*Server, error) {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8099"
	}
	if cfg.DBPath == "" {
		cfg.DBPath = "./mono.db"
	}
	if cfg.TokenTTL <= 0 {
		cfg.TokenTTL = 24 * time.Hour
	}
	if cfg.HeartbeatTimeout <= 0 {
		cfg.HeartbeatTimeout = 5 * time.Second
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = 10 * time.Second
	}

	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		return nil, err
	}
	if err := initSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	s := &Server{
		cfg:         cfg,
		logger:      logger,
		db:          db,
		rooms:       make(map[string]*Room),
		playerRoom:  make(map[string]string),
		gameStates:  make(map[string]map[string]*gameplay.PlayerState),
		roomConns:   make(map[string]map[string]*websocket.Conn),
		lastHB:      make(map[string]int64),
		roomTickets: make(map[string]map[string]string),
		roomNextAdd: make(map[string]int64),
		lobbyWSSubs: make(map[*websocket.Conn]struct{}),
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
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/lobby/rooms", s.handleRooms)
	mux.HandleFunc("/api/lobby/join", s.handleJoin)
	mux.HandleFunc("/api/lobby/leave", s.handleLeave)
	mux.HandleFunc("/api/lobby/room", s.handleRoomDetail)
	mux.HandleFunc("/api/lobby/start", s.handleStart)
	mux.HandleFunc("/ws/lobby", s.handleWSLobby)
	mux.HandleFunc("/ws/room", s.handleWSRoom)
	s.httpSrv = &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go s.heartbeatLoop()
	return s, nil
}

func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return err
	}
	s.listener = ln
	s.logger.Info("mono server started", "addr", s.cfg.ListenAddr, "db", s.cfg.DBPath)
	return s.httpSrv.Serve(ln)
}

func (s *Server) Stop(ctx context.Context) error {
	if s.listener != nil {
		_ = s.listener.Close()
	}
	_ = s.httpSrv.Shutdown(ctx)
	return s.db.Close()
}

func initSchema(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			password TEXT NOT NULL,
			created_at INTEGER NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS tokens (
			token TEXT PRIMARY KEY,
			player_id TEXT NOT NULL,
			expire_at INTEGER NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_tokens_player ON tokens(player_id);`,
	}
	for _, st := range stmts {
		if _, err := db.Exec(st); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte(`{"status":"ok"}`))
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
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		http.Error(w, "missing username/password", http.StatusBadRequest)
		return
	}
	if err := s.ensureUser(req.Username, req.Password); err != nil {
		http.Error(w, "login failed", http.StatusUnauthorized)
		return
	}
	token := randHex(16)
	exp := time.Now().Add(s.cfg.TokenTTL).Unix()
	_, _ = s.db.Exec(`INSERT INTO tokens(token, player_id, expire_at) VALUES(?,?,?) ON CONFLICT(token) DO UPDATE SET player_id=excluded.player_id, expire_at=excluded.expire_at`, token, req.Username, exp)
	http.SetCookie(w, &http.Cookie{
		Name:     "token",
		Value:    token,
		Path:     "/",
		HttpOnly: false,
	})
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token":      token,
		"player_id":  req.Username,
		"expires_in": int64(s.cfg.TokenTTL.Seconds()),
	})
}

func (s *Server) ensureUser(username, password string) error {
	var exists int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM users WHERE username=?`, username).Scan(&exists)
	if err != nil {
		return err
	}
	if exists == 0 {
		_, err = s.db.Exec(`INSERT INTO users(username,password,created_at) VALUES(?,?,?)`, username, password, time.Now().Unix())
		return err
	}
	var dbPwd string
	if err := s.db.QueryRow(`SELECT password FROM users WHERE username=?`, username).Scan(&dbPwd); err != nil {
		return err
	}
	if dbPwd != password {
		return errors.New("bad password")
	}
	return nil
}

func (s *Server) authPlayer(r *http.Request) (string, error) {
	token := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		token = strings.TrimSpace(token[7:])
	}
	if token == "" {
		if c, err := r.Cookie("token"); err == nil {
			token = strings.TrimSpace(c.Value)
		}
	}
	if token == "" {
		token = strings.TrimSpace(r.URL.Query().Get("token"))
	}
	if token == "" {
		return "", errors.New("missing token")
	}
	var pid string
	var exp int64
	err := s.db.QueryRow(`SELECT player_id, expire_at FROM tokens WHERE token=?`, token).Scan(&pid, &exp)
	if err != nil {
		return "", errors.New("invalid token")
	}
	if exp < time.Now().Unix() {
		return "", errors.New("token expired")
	}
	newExp := time.Now().Add(s.cfg.TokenTTL).Unix()
	_, _ = s.db.Exec(`UPDATE tokens SET expire_at=? WHERE token=?`, newExp, token)
	return pid, nil
}

func (s *Server) handleRooms(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listRooms(w)
	case http.MethodPost:
		s.createRoom(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) listRooms(w http.ResponseWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, 0, len(s.rooms))
	for _, room := range s.rooms {
		out = append(out, map[string]any{
			"room_id": room.ID, "name": room.Name, "owner_id": room.OwnerID, "state": room.State, "winner_id": room.WinnerID,
			"player_cnt": len(room.Players), "max_players": room.MaxPlayers,
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "rooms": out})
}

func (s *Server) createRoom(w http.ResponseWriter, r *http.Request) {
	pid, err := s.authPlayer(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		Name       string `json:"name"`
		MaxPlayers int    `json:"max_players"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.MaxPlayers < 2 {
		req.MaxPlayers = 2
	}
	if req.MaxPlayers > 8 {
		req.MaxPlayers = 8
	}
	if req.Name == "" {
		req.Name = "Room"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if rid, ok := s.playerRoom[pid]; ok {
		s.removePlayerLocked(pid, rid)
	}
	rid := strconv.FormatInt(time.Now().UnixNano(), 10)
	room := &Room{ID: rid, Name: req.Name, OwnerID: pid, State: "waiting", MaxPlayers: req.MaxPlayers, Players: map[string]struct{}{pid: {}}}
	s.rooms[rid] = room
	s.playerRoom[pid] = rid
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "room": map[string]any{
		"room_id": rid, "name": room.Name, "owner_id": room.OwnerID, "state": room.State, "winner_id": room.WinnerID, "player_cnt": 1, "max_players": room.MaxPlayers,
	}})
	s.broadcastLobbyRoomsUpdated(rid, pid)
}

func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pid, err := s.authPlayer(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		RoomID string `json:"room_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	s.mu.Lock()
	defer s.mu.Unlock()
	room, ok := s.rooms[req.RoomID]
	if !ok {
		http.Error(w, "room not found", http.StatusNotFound)
		return
	}
	if _, ok := room.Players[pid]; ok {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0})
		return
	}
	if len(room.Players) >= room.MaxPlayers {
		http.Error(w, "room full", http.StatusConflict)
		return
	}
	room.Players[pid] = struct{}{}
	s.playerRoom[pid] = room.ID
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0})
	s.broadcastLobbyRoomsUpdated(room.ID, pid)
}

func (s *Server) handleLeave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pid, err := s.authPlayer(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rid := s.playerRoom[pid]
	if rid != "" {
		s.removePlayerLocked(pid, rid)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "next_page": "/lobby"})
	s.broadcastLobbyRoomsUpdated(rid, pid)
}

func (s *Server) handleRoomDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pid, err := s.authPlayer(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rid := strings.TrimSpace(r.URL.Query().Get("room_id"))
	s.mu.Lock()
	defer s.mu.Unlock()
	room, ok := s.rooms[rid]
	if !ok {
		http.Error(w, "room not found", http.StatusNotFound)
		return
	}
	if s.playerRoom[pid] != rid {
		http.Error(w, "player not in room", http.StatusForbidden)
		return
	}
	members := make([]string, 0, len(room.Players))
	for p := range room.Players {
		members = append(members, p)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code": 0,
		"room": map[string]any{
			"room_id": rid, "name": room.Name, "owner_id": room.OwnerID, "state": room.State, "winner_id": room.WinnerID,
			"player_cnt": len(room.Players), "max_players": room.MaxPlayers,
		},
		"members":   members,
		"is_owner":  pid == room.OwnerID,
		"game_addr": r.Host,
		"game_ticket": func() string {
			if ts := s.roomTickets[rid]; ts != nil {
				return ts[pid]
			}
			return ""
		}(),
	})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pid, err := s.authPlayer(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		RoomID string `json:"room_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	s.mu.Lock()
	defer s.mu.Unlock()
	room, ok := s.rooms[req.RoomID]
	if !ok {
		http.Error(w, "room not found", http.StatusNotFound)
		return
	}
	if room.OwnerID != pid {
		http.Error(w, "only owner can start", http.StatusForbidden)
		return
	}
	if len(room.Players) < 2 {
		http.Error(w, "need at least 2 players", http.StatusConflict)
		return
	}
	room.State = "playing"
	room.WinnerID = ""
	s.gameStates[room.ID] = make(map[string]*gameplay.PlayerState)
	s.roomTickets[room.ID] = make(map[string]string, len(room.Players))
	for p := range room.Players {
		s.gameStates[room.ID][p] = gameplay.NewInitialState()
		s.roomTickets[room.ID][p] = randHex(8)
	}
	s.roomNextAdd[room.ID] = time.Now().UnixMilli() + 10_000
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":        0,
		"msg":         "started",
		"game_addr":   r.Host,
		"game_ticket": s.roomTickets[room.ID][pid],
	})
	s.broadcastLobbyRoomsUpdated(room.ID, pid)
}

var upgrader = websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}

func (s *Server) handleWSRoom(w http.ResponseWriter, r *http.Request) {
	pid, err := s.authPlayer(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	roomID := strings.TrimSpace(r.URL.Query().Get("room_id"))
	if roomID == "" {
		http.Error(w, "missing room_id", http.StatusBadRequest)
		return
	}
	ticket := strings.TrimSpace(r.URL.Query().Get("ticket"))
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	s.mu.Lock()
	if s.playerRoom[pid] != roomID {
		s.mu.Unlock()
		_ = conn.WriteJSON(map[string]any{"type": "error", "msg": "player not in room"})
		_ = conn.Close()
		return
	}
	room := s.rooms[roomID]
	if room != nil && room.State == "playing" {
		if ts := s.roomTickets[roomID]; ts != nil {
			if ts[pid] == "" || ts[pid] != ticket {
				s.mu.Unlock()
				_ = conn.WriteJSON(map[string]any{"type": "error", "msg": "invalid session or ticket"})
				_ = conn.Close()
				return
			}
		}
	}
	s.mu.Unlock()

	s.mu.Lock()
	if _, ok := s.roomConns[roomID]; !ok {
		s.roomConns[roomID] = make(map[string]*websocket.Conn)
	}
	if old := s.roomConns[roomID][pid]; old != nil {
		_ = old.Close()
	}
	s.roomConns[roomID][pid] = conn
	s.lastHB[pid] = time.Now().UnixMilli()
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		if m := s.roomConns[roomID]; m != nil {
			delete(m, pid)
			if len(m) == 0 {
				delete(s.roomConns, roomID)
			}
		}
		s.mu.Unlock()
		_ = conn.Close()
	}()

	done := make(chan struct{})
	go s.readPlayerWS(conn, roomID, pid, done)
	tk := time.NewTicker(70 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-done:
			return
		case <-tk.C:
			st, ok := s.buildState(roomID, pid)
			if !ok {
				return
			}
			if err := conn.WriteJSON(st); err != nil {
				return
			}
		}
	}
}

func (s *Server) readPlayerWS(conn *websocket.Conn, roomID, pid string, done chan struct{}) {
	defer close(done)
	for {
		var req map[string]any
		if err := conn.ReadJSON(&req); err != nil {
			return
		}
		s.mu.Lock()
		s.lastHB[pid] = time.Now().UnixMilli()
		states := s.gameStates[roomID]
		if states != nil {
			st := states[pid]
			if st != nil {
				switch req["type"] {
				case "aim":
					if a, ok := req["angle"].(float64); ok {
						st.AimAngle = gameplay.ClampAim(a)
					}
				case "shot":
					if a, ok := req["angle"].(float64); ok {
						_ = gameplay.BeginClientShot(st, a, time.Now().UnixMilli())
					}
				case "land":
					a, _ := req["angle"].(float64)
					x, _ := req["x"].(float64)
					y, _ := req["y"].(float64)
					_ = gameplay.CommitLanding(st, a, x, y)
					s.resolveRoomOutcomeLocked(roomID)
				}
			}
		}
		s.mu.Unlock()
	}
}

func (s *Server) buildState(roomID, pid string) (map[string]any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	room := s.rooms[roomID]
	if room == nil {
		return nil, false
	}
	states := s.gameStates[roomID]
	if states == nil {
		// Keep payload type as "state" so frontend can reuse one transition branch.
		return map[string]any{
			"type":   "state",
			"room":   map[string]any{"room_id": roomID, "state": room.State},
			"self":   nil,
			"others": []map[string]any{},
		}, true
	}
	now := time.Now().UnixMilli()
	for _, st := range states {
		gameplay.TickState(st, now)
	}
	s.resolveRoomOutcomeLocked(roomID)
	s.maybeAddTopLayerLocked(roomID, now)
	self := gameplay.CloneState(states[pid])
	others := make([]map[string]any, 0)
	for op, st := range states {
		if op == pid {
			continue
		}
		others = append(others, map[string]any{"player_id": op, "state": gameplay.CloneState(st)})
	}
	return map[string]any{
		"type": "state",
		"room": map[string]any{"room_id": roomID, "state": room.State, "winner_id": room.WinnerID},
		"self": self, "others": others,
	}, true
}

func (s *Server) heartbeatLoop() {
	tk := time.NewTicker(1 * time.Second)
	defer tk.Stop()
	timeoutMs := s.cfg.HeartbeatTimeout.Milliseconds()
	if timeoutMs <= 0 {
		timeoutMs = 5000
	}
	for range tk.C {
		now := time.Now().UnixMilli()
		toLeave := make([]string, 0)
		s.mu.Lock()
		for pid, hb := range s.lastHB {
			if now-hb > timeoutMs {
				toLeave = append(toLeave, pid)
			}
		}
		for _, pid := range toLeave {
			rid := s.playerRoom[pid]
			if rid != "" {
				s.removePlayerLocked(pid, rid)
				s.broadcastLobbyRoomsUpdated(rid, pid)
			}
			delete(s.lastHB, pid)
		}
		s.mu.Unlock()
	}
}

func (s *Server) removePlayerLocked(pid, rid string) {
	room := s.rooms[rid]
	if room == nil {
		delete(s.playerRoom, pid)
		return
	}
	delete(room.Players, pid)
	delete(s.playerRoom, pid)
	if c := s.roomConns[rid][pid]; c != nil {
		_ = c.Close()
	}
	if len(room.Players) == 0 {
		delete(s.rooms, rid)
		delete(s.gameStates, rid)
		delete(s.roomTickets, rid)
		delete(s.roomConns, rid)
		delete(s.roomNextAdd, rid)
		return
	}
	if room.State == "playing" && len(room.Players) <= 1 {
		room.State = "waiting"
		room.WinnerID = ""
		for remain := range room.Players {
			if st := s.gameStates[rid][remain]; st != nil && st.Status != "lose" {
				room.WinnerID = remain
				st.Status = "win"
				break
			}
		}
		delete(s.gameStates, rid)
		delete(s.roomTickets, rid)
		delete(s.roomNextAdd, rid)
	}
	if room.OwnerID == pid {
		for np := range room.Players {
			room.OwnerID = np
			break
		}
	}
}

func (s *Server) resolveRoomOutcomeLocked(roomID string) {
	room := s.rooms[roomID]
	if room == nil || room.State != "playing" {
		return
	}
	states := s.gameStates[roomID]
	if states == nil || len(room.Players) < 2 {
		return
	}
	alive := make([]string, 0, len(room.Players))
	losers := 0
	for pid := range room.Players {
		st := states[pid]
		if st == nil {
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
		if st == nil {
			continue
		}
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
	delete(s.gameStates, roomID)
	delete(s.roomTickets, roomID)
	delete(s.roomNextAdd, roomID)
}

func (s *Server) maybeAddTopLayerLocked(roomID string, now int64) {
	room := s.rooms[roomID]
	if room == nil || room.State != "playing" {
		delete(s.roomNextAdd, roomID)
		return
	}
	states := s.gameStates[roomID]
	if len(states) == 0 {
		return
	}
	nextAt := s.roomNextAdd[roomID]
	if nextAt <= 0 {
		s.roomNextAdd[roomID] = now + 10_000
		return
	}
	if now >= nextAt && !canAddLayer(states) {
		return
	}
	for now >= nextAt {
		if !canAddLayer(states) {
			break
		}
		for _, st := range states {
			if st != nil {
				gameplay.AddTopLayer(st)
			}
		}
		nextAt += 10_000
	}
	s.roomNextAdd[roomID] = nextAt
}

func canAddLayer(states map[string]*gameplay.PlayerState) bool {
	if len(states) == 0 {
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

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) handleWSLobby(w http.ResponseWriter, r *http.Request) {
	if _, err := s.authPlayer(r); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	s.lobbyMu.Lock()
	s.lobbyWSSubs[conn] = struct{}{}
	s.lobbyMu.Unlock()

	defer func() {
		s.lobbyMu.Lock()
		delete(s.lobbyWSSubs, conn)
		s.lobbyMu.Unlock()
		_ = conn.Close()
	}()

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (s *Server) broadcastLobbyRoomsUpdated(roomID, playerID string) {
	evt := map[string]any{
		"type":      "lobby_rooms_updated",
		"room_id":   roomID,
		"player_id": playerID,
		"ts":        time.Now().UnixMilli(),
	}
	data, err := json.Marshal(evt)
	if err != nil {
		return
	}
	s.lobbyMu.Lock()
	conns := make([]*websocket.Conn, 0, len(s.lobbyWSSubs))
	for c := range s.lobbyWSSubs {
		conns = append(conns, c)
	}
	s.lobbyMu.Unlock()
	for _, c := range conns {
		if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
			s.lobbyMu.Lock()
			delete(s.lobbyWSSubs, c)
			s.lobbyMu.Unlock()
			_ = c.Close()
		}
	}
}
