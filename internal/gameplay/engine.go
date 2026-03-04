package gameplay

import (
	"errors"
	"math"
	"math/rand"
	"time"
)

const (
	gbRows     = 14
	gbCols     = 17
	gbRadius   = 16.0
	gbDiam     = gbRadius * 2
	gbRowGap   = 28.0
	gbBoardW   = 560.0
	gbShooterY = 760.0 - 72.0
	gbShootV   = 520.0
	gbMinAngle = -math.Pi + 0.2
	gbMaxAngle = -0.2
	gbMaxDt    = 0.033
)

type MovingBubble struct {
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	VX    float64 `json:"vx"`
	VY    float64 `json:"vy"`
	Color int     `json:"color"`
}

type PlayerState struct {
	Board        [][]int       `json:"board"`
	CurrentColor int           `json:"current_color"`
	NextColor    int           `json:"next_color"`
	AimAngle     float64       `json:"aim_angle"`
	Moving       *MovingBubble `json:"moving,omitempty"`
	PendingShot  bool          `json:"pending_shot"`
	ShotSeq      int64         `json:"shot_seq"`
	ShotAngle    float64       `json:"shot_angle"`
	ShotColor    int           `json:"shot_color"`
	ShotAtMS     int64         `json:"shot_at_ms"`
	Score        int           `json:"score"`
	Status       string        `json:"status"` // playing/win/lose
	LastTickMS   int64         `json:"last_tick_ms"`
}

// NewInitialState 为单个玩家创建随机初始化的棋盘与发射器状态。
func NewInitialState() *PlayerState {
	now := time.Now().UnixMilli()
	st := &PlayerState{
		Board:        make([][]int, gbRows),
		CurrentColor: rand.Intn(6),
		NextColor:    rand.Intn(6),
		AimAngle:     -math.Pi / 2,
		Moving:       nil,
		PendingShot:  false,
		ShotSeq:      0,
		ShotAngle:    -math.Pi / 2,
		ShotColor:    0,
		ShotAtMS:     0,
		Score:        0,
		Status:       "playing",
		LastTickMS:   now,
	}
	for r := 0; r < gbRows; r++ {
		st.Board[r] = make([]int, gbCols)
		for c := 0; c < gbCols; c++ {
			st.Board[r][c] = -1
		}
	}
	for r := 0; r < 5; r++ {
		for c := 0; c < gbCols; c++ {
			if rand.Float64() < 0.78 {
				st.Board[r][c] = rand.Intn(6)
			}
		}
	}
	return st
}

// TickState 将单个玩家的模拟按时间推进到 nowMS。
func TickState(st *PlayerState, nowMS int64) {
	if st == nil {
		return
	}
	if st.Status != "playing" {
		st.LastTickMS = nowMS
		return
	}
	dt := float64(nowMS-st.LastTickMS) / 1000.0
	if dt < 0 {
		dt = 0
	}
	if dt > gbMaxDt {
		dt = gbMaxDt
	}
	st.LastTickMS = nowMS
	if st.Moving == nil {
		return
	}
	m := st.Moving
	m.X += m.VX * dt
	m.Y += m.VY * dt

	if m.X <= gbRadius {
		m.X = gbRadius
		m.VX = math.Abs(m.VX)
	} else if m.X >= gbBoardW-gbRadius {
		m.X = gbBoardW - gbRadius
		m.VX = -math.Abs(m.VX)
	}

	if m.Y <= gbRadius || collideBoard(st.Board, m.X, m.Y) {
		placeAndResolve(st)
	}
}

// addTopLayer 将棋盘整体下压一行，并在顶行生成一层随机颜色泡泡。
func AddTopLayer(st *PlayerState) {
	newBoard := make([][]int, gbRows)
	for r := 0; r < gbRows; r++ {
		newBoard[r] = make([]int, gbCols)
		for c := 0; c < gbCols; c++ {
			newBoard[r][c] = -1
		}
	}

	// Reproject each bubble to the next row by world-x position, so odd/even row offset
	// transitions stay stable when adding layers.
	for r := gbRows - 2; r >= 0; r-- {
		for c := 0; c < gbCols; c++ {
			color := st.Board[r][c]
			if color < 0 {
				continue
			}
			targetRow := r + 1
			x, _ := cellCenter(r, c)
			targetCol := nearestColForRowX(targetRow, x)
			if newBoard[targetRow][targetCol] >= 0 {
				if alt, ok := nearestEmptyInRow(newBoard[targetRow], targetCol); ok {
					targetCol = alt
				} else {
					continue
				}
			}
			newBoard[targetRow][targetCol] = color
		}
	}

	for c := 0; c < gbCols; c++ {
		newBoard[0][c] = rand.Intn(6)
	}
	st.Board = newBoard
	checkState(st)
}

func nearestColForRowX(row int, x float64) int {
	shift := 0.0
	if row%2 == 1 {
		shift = gbRadius
	}
	col := int(math.Round((x - gbRadius - shift) / gbDiam))
	if col < 0 {
		return 0
	}
	if col >= gbCols {
		return gbCols - 1
	}
	return col
}

func nearestEmptyInRow(row []int, center int) (int, bool) {
	if center >= 0 && center < len(row) && row[center] < 0 {
		return center, true
	}
	for d := 1; d < len(row); d++ {
		l := center - d
		if l >= 0 && row[l] < 0 {
			return l, true
		}
		r := center + d
		if r < len(row) && row[r] < 0 {
			return r, true
		}
	}
	return -1, false
}

// Fire 在玩家可发射时按给定角度发射当前泡泡。
func Fire(st *PlayerState, angle float64) error {
	if st == nil {
		return errors.New("state not found")
	}
	if st.Status != "playing" {
		return errors.New("game already finished")
	}
	if st.Moving != nil {
		return errors.New("bubble_in_flight")
	}

	angle = ClampAim(angle)
	st.AimAngle = angle
	st.Moving = &MovingBubble{
		X:     gbBoardW / 2,
		Y:     gbShooterY,
		VX:    math.Cos(angle) * gbShootV,
		VY:    math.Sin(angle) * gbShootV,
		Color: st.CurrentColor,
	}
	return nil
}

// BeginClientShot 记录一次客户端发射开始，用于给其他玩家同步轨迹。
func BeginClientShot(st *PlayerState, angle float64, nowMS int64) error {
	if st == nil {
		return errors.New("state not found")
	}
	if st.Status != "playing" {
		return errors.New("game already finished")
	}
	if st.Moving != nil || st.PendingShot {
		return errors.New("bubble_in_flight")
	}
	angle = ClampAim(angle)
	st.AimAngle = angle
	st.PendingShot = true
	st.ShotSeq++
	st.ShotAngle = angle
	st.ShotColor = st.CurrentColor
	st.ShotAtMS = nowMS
	return nil
}

// CommitLanding 使用客户端上报的落点来落子，并在服务端结算消除/掉落/胜负。
func CommitLanding(st *PlayerState, angle, x, y float64) error {
	if st == nil {
		return errors.New("state not found")
	}
	if st.Status != "playing" {
		return errors.New("game already finished")
	}
	if !st.PendingShot || st.Moving != nil {
		return errors.New("no_pending_shot")
	}
	if x < 0 || x > gbBoardW || y < 0 || y > gbShooterY+gbRadius {
		return errors.New("invalid landing position")
	}
	st.AimAngle = ClampAim(angle)
	color := st.ShotColor
	st.PendingShot = false
	st.Moving = &MovingBubble{
		X:     x,
		Y:     y,
		Color: color,
	}
	placeAndResolve(st)
	return nil
}

// ClampAim 将瞄准角限制在发射器允许范围内。
func ClampAim(angle float64) float64 {
	if angle < gbMinAngle {
		return gbMinAngle
	}
	if angle > gbMaxAngle {
		return gbMaxAngle
	}
	return angle
}

// CloneState 返回可直接用于网络同步的状态深拷贝。
func CloneState(st *PlayerState) map[string]any {
	board := make([][]int, len(st.Board))
	for i := range st.Board {
		board[i] = append([]int(nil), st.Board[i]...)
	}
	var moving map[string]any
	if st.Moving != nil {
		moving = map[string]any{
			"x":     st.Moving.X,
			"y":     st.Moving.Y,
			"vx":    st.Moving.VX,
			"vy":    st.Moving.VY,
			"color": st.Moving.Color,
		}
	}
	return map[string]any{
		"board":         board,
		"current_color": st.CurrentColor,
		"next_color":    st.NextColor,
		"aim_angle":     st.AimAngle,
		"moving":        moving,
		"pending_shot":  st.PendingShot,
		"shot_seq":      st.ShotSeq,
		"shot_angle":    st.ShotAngle,
		"shot_color":    st.ShotColor,
		"shot_at_ms":    st.ShotAtMS,
		"score":         st.Score,
		"state":         st.Status,
	}
}

// collideBoard 检查移动泡泡是否与棋盘上已占用格子发生重叠碰撞。
func collideBoard(board [][]int, x, y float64) bool {
	for r := 0; r < gbRows; r++ {
		for c := 0; c < gbCols; c++ {
			if board[r][c] < 0 {
				continue
			}
			cx, cy := cellCenter(r, c)
			dx := x - cx
			dy := y - cy
			if dx*dx+dy*dy <= (gbDiam-2)*(gbDiam-2) {
				return true
			}
		}
	}
	return false
}

// placeAndResolve 将移动泡泡吸附到网格并结算消除与掉落。
func placeAndResolve(st *PlayerState) {
	if st.Moving == nil {
		return
	}
	row, col := nearestCell(st.Moving.X, st.Moving.Y)
	row, col = clampCell(row, col)
	if st.Board[row][col] >= 0 {
		if nr, nc, ok := findNearestEmpty(st.Board, row, col, st.Moving.X, st.Moving.Y); ok {
			row, col = nr, nc
		} else {
			st.Moving = nil
			return
		}
	}
	st.Board[row][col] = st.Moving.Color
	st.Moving = nil

	removed := clearMatches(st.Board, row, col)
	if removed > 0 {
		st.Score += removed * 10
	}
	dropped := dropFloating(st.Board)
	if dropped > 0 {
		st.Score += dropped * 15
	}

	st.CurrentColor = st.NextColor
	st.NextColor = rand.Intn(6)
	checkState(st)
}

// checkState 根据剩余泡泡和底线情况更新胜负与进行中状态。
func checkState(st *PlayerState) {
	cnt := 0
	for r := 0; r < gbRows; r++ {
		for c := 0; c < gbCols; c++ {
			if st.Board[r][c] >= 0 {
				cnt++
			}
		}
	}
	if cnt == 0 {
		st.Status = "win"
		return
	}
	for c := 0; c < gbCols; c++ {
		if st.Board[gbRows-1][c] >= 0 {
			st.Status = "lose"
			return
		}
	}
	st.Status = "playing"
}

// neighbors 返回六边形网格中某个格子的相邻格坐标。
func neighbors(r, c int) [][2]int {
	if r%2 == 1 {
		return [][2]int{{r - 1, c}, {r - 1, c + 1}, {r, c - 1}, {r, c + 1}, {r + 1, c}, {r + 1, c + 1}}
	}
	return [][2]int{{r - 1, c - 1}, {r - 1, c}, {r, c - 1}, {r, c + 1}, {r + 1, c - 1}, {r + 1, c}}
}

// clearMatches 在同色连通数量不少于 3 时执行消除并返回消除数量。
func clearMatches(board [][]int, sr, sc int) int {
	color := board[sr][sc]
	if color < 0 {
		return 0
	}
	type pt struct{ r, c int }
	q := []pt{{sr, sc}}
	vis := map[[2]int]struct{}{{sr, sc}: {}}
	group := make([]pt, 0, 16)
	for len(q) > 0 {
		p := q[0]
		q = q[1:]
		group = append(group, p)
		for _, n := range neighbors(p.r, p.c) {
			r, c := n[0], n[1]
			if !inBoard(r, c) || board[r][c] != color {
				continue
			}
			key := [2]int{r, c}
			if _, ok := vis[key]; ok {
				continue
			}
			vis[key] = struct{}{}
			q = append(q, pt{r, c})
		}
	}
	if len(group) < 3 {
		return 0
	}
	for _, p := range group {
		board[p.r][p.c] = -1
	}
	return len(group)
}

// dropFloating 移除与顶行不连通的悬空泡泡并返回移除数量。
func dropFloating(board [][]int) int {
	type pt struct{ r, c int }
	q := make([]pt, 0, 64)
	vis := map[[2]int]struct{}{}
	for c := 0; c < gbCols; c++ {
		if board[0][c] >= 0 {
			q = append(q, pt{0, c})
			vis[[2]int{0, c}] = struct{}{}
		}
	}
	for len(q) > 0 {
		p := q[0]
		q = q[1:]
		for _, n := range neighbors(p.r, p.c) {
			r, c := n[0], n[1]
			if !inBoard(r, c) || board[r][c] < 0 {
				continue
			}
			k := [2]int{r, c}
			if _, ok := vis[k]; ok {
				continue
			}
			vis[k] = struct{}{}
			q = append(q, pt{r, c})
		}
	}
	dropped := 0
	for r := 0; r < gbRows; r++ {
		for c := 0; c < gbCols; c++ {
			if board[r][c] < 0 {
				continue
			}
			if _, ok := vis[[2]int{r, c}]; !ok {
				board[r][c] = -1
				dropped++
			}
		}
	}
	return dropped
}

// nearestCell 将世界坐标映射到最近的棋盘逻辑格子。
func nearestCell(x, y float64) (int, int) {
	row := int(math.Round((y - gbRadius) / gbRowGap))
	if row < 0 {
		row = 0
	}
	if row >= gbRows {
		row = gbRows - 1
	}
	shift := 0.0
	if row%2 == 1 {
		shift = gbRadius
	}
	col := int(math.Round((x - gbRadius - shift) / gbDiam))
	if col < 0 {
		col = 0
	}
	if col >= gbCols {
		col = gbCols - 1
	}
	return row, col
}

// clampCell 将行列索引限制在棋盘边界内。
func clampCell(row, col int) (int, int) {
	if row < 0 {
		row = 0
	}
	if row >= gbRows {
		row = gbRows - 1
	}
	if col < 0 {
		col = 0
	}
	if col >= gbCols {
		col = gbCols - 1
	}
	return row, col
}

// findNearestEmpty 在目标格被占用时，寻找其附近最近的空格。
func findNearestEmpty(board [][]int, baseR, baseC int, x, y float64) (int, int, bool) {
	bestR, bestC := -1, -1
	best := math.MaxFloat64
	for dr := -2; dr <= 2; dr++ {
		for dc := -2; dc <= 2; dc++ {
			r := baseR + dr
			c := baseC + dc
			if !inBoard(r, c) || board[r][c] >= 0 {
				continue
			}
			cx, cy := cellCenter(r, c)
			dx := cx - x
			dy := cy - y
			d2 := dx*dx + dy*dy
			if d2 < best {
				best = d2
				bestR, bestC = r, c
			}
		}
	}
	return bestR, bestC, bestR >= 0
}

// inBoard 判断给定行列坐标是否在棋盘范围内。
func inBoard(r, c int) bool {
	return r >= 0 && r < gbRows && c >= 0 && c < gbCols
}

// cellCenter 将棋盘格坐标转换为世界坐标中的中心点位置。
func cellCenter(row, col int) (float64, float64) {
	shift := 0.0
	if row%2 == 1 {
		shift = gbRadius
	}
	return gbRadius + shift + float64(col)*gbDiam, gbRadius + float64(row)*gbRowGap
}
