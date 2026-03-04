# 游戏项目 README（部署与页面说明）

本项目当前采用“UI + Auth + Gateway + Game(Room 合并)”架构，所有服务启动配置统一从 JSON 文件读取。

## 演示视频

<video src="./video.mp4" controls width="960">
  你的 Markdown 渲染器不支持 video 标签，可直接打开项目根目录的 video.mp4。
</video>

## 1. 项目结构与职责

- `cmd/auth`：鉴权服务，负责登录与 token 校验（HTTP + gRPC）。
- `cmd/gateway`：网关服务，负责客户端实时推送通道（TCP/WS）与对外 gRPC 推送入口。
- `cmd/game`：房间与游戏服务（同一服务），负责房间状态、房间心跳、游戏会话、游戏帧同步。
- `cmd/ui`：页面与 HTTP API 服务，负责登录页/大厅/房间/游戏页面、以及页面调用的后端接口。
- `cmd/mono`：单体版本（内置 UI + API + WS + SQLite），用于快速本地体验。

## 2. 依赖要求

- Go `1.25+`
- Redis（Auth token 存储）
- MySQL（按需，用于 schema 初始化与相关模块）
- Windows/Linux 均可

## 3. 默认端口

- UI：`8088`
- Auth：HTTP `19080`，gRPC `19090`
- Gateway：TCP `7000`，WS `18081`，gRPC Push `18080`
- Game（含 Room）：WS `19500`，gRPC `19400`
- Mono：`8099`

## 4. 配置文件

目录：`configs/`

- `auth.json`
- `gateway.json`
- `game.json`
- `ui.json`
- `mono.json`
- `lobby.json`
- `match.json`
- `room.json`
- `usersystem.json`
- `dbinit.json`

说明：所有服务都通过 `-config` 指定 JSON 文件，不再依赖环境变量。

## 5. 编译

```powershell
go build ./...
```

如果 Go 默认缓存目录权限受限，可改用项目本地缓存：

```powershell
$env:GOCACHE='D:\work\game\.gocache'
$env:GOMODCACHE='D:\work\game\.gomodcache'
go build ./...
```

## 6. 启动方式

### 6.1 微服务模式

按顺序启动：

```powershell
go run ./cmd/auth -config configs/auth.json
```

```powershell
go run ./cmd/gateway -config configs/gateway.json
```

```powershell
go run ./cmd/game -config configs/game.json
```

```powershell
go run ./cmd/ui -config configs/ui.json
```

UI 入口：`http://127.0.0.1:8088/`

### 6.2 单体模式

```powershell
go run ./cmd/mono -config configs/mono.json
```

入口：

- `http://127.0.0.1:8099/`
- 健康检查：`http://127.0.0.1:8099/healthz`

## 7. 页面 URL 说明（重点）

以下页面由 `ui` 服务提供：

1. 登录页：`GET /`
- 对应文件：`web/login.html`
- 功能：输入用户名/密码，调用 `POST /api/login`。
- 登录成功后写入本地存储：`token`、`player_id`，然后跳转 `/lobby`。

2. 大厅页：`GET /lobby`
- 对应文件：`web/lobby.html`
- 功能：拉取房间列表、创建房间、加入房间。
- 实时更新：通过 Gateway WS `ws://<gateway>/ws/lobby?token=...` 接收房间列表更新通知。
- 常见跳转：创建/加入成功后跳转 `/room`。

3. 房间页：`GET /room`
- 对应文件：`web/room.html`
- 功能：显示成员、房主操作（开始游戏）、离开房间。
- 房主点击开始：调用 `POST /api/lobby/start`。
- 开始成功后获取 `game_addr + game_ticket`，跳转 `/game`。
- 房间不存在或玩家不在房间时：回到 `/lobby`。

4. 游戏页：`GET /game`
- 对应文件：`web/game.html`
- 功能：泡泡龙主战斗界面，含本地玩家画布与 OTHER PLAYERS 小窗。
- 数据通道：连接 Game WS `/ws/room`，进行心跳、瞄准同步、发射/落点同步、状态回包。
- 游戏结束后：跳回 `/room`（如胜负结算、只剩一人、游戏停止）。

## 8. UI 提供的 HTTP API

### 8.1 登录与大厅

1. `POST /api/login`
- 入参：`{ username, password }`
- 出参：`{ token, player_id, expires_in }`

2. `GET /api/lobby/rooms`
- 功能：获取房间列表。

3. `POST /api/lobby/rooms`
- 功能：创建房间并自动加入。
- 请求头：`X-Player-Id`
- 入参：`{ name, max_players }`

4. `POST /api/lobby/join`
- 功能：加入房间。
- 请求头：`X-Player-Id`
- 入参：`{ room_id }`

5. `GET /api/lobby/room?room_id=...`
- 功能：查询房间详情、成员、是否房主、游戏连接信息（若已有）。
- 请求头：`X-Player-Id`

6. `POST /api/lobby/start`
- 功能：房主开始游戏（要求房间至少 2 人）。
- 请求头：`X-Player-Id`
- 入参：`{ room_id }`

7. `POST /api/lobby/leave`
- 功能：离开房间。
- 请求头：`X-Player-Id`

8. `GET /api/lobby/room/events?room_id=...&player_id=...`
- 功能：SSE 房间事件流（房间更新、删除、开局等）。

### 8.2 游戏状态（UI 辅助接口）

1. `GET /api/lobby/game/state?room_id=...`
- 功能：拉取当前玩家与其他玩家状态快照。

2. `POST /api/lobby/game/fire`
- 功能：发射请求（部分路径仍保留，主同步已由 WS 承担）。

## 9. WebSocket 通道说明

1. Gateway Lobby WS
- 地址：`ws://<gateway_host>:18081/ws/lobby?token=...`
- 用途：大厅房间列表实时更新通知。

2. Game Room WS
- 地址：`ws://<game_host>:19500/ws/room?room_id=...&player_id=...&ticket=...&token=...`
- 用途：房间心跳、游戏同步（aim/shot/land/state/game_over）。

## 10. 页面跳转规则

- 未登录（缺 `token`/`player_id`）访问大厅/房间/游戏：重定向到 `/`。
- 房间页发现房间不存在或玩家不在房间：跳 `/lobby`。
- 游戏页发现无 `game_addr` 或 `game_ticket`：跳 `/room`。
- 游戏 WS 返回 `invalid session or ticket`：跳 `/room`。
- 游戏中房间解散或成员状态异常：跳 `/lobby` 或 `/room`（按返回事件）。

## 11. 数据库初始化

执行：

```powershell
go run ./cmd/dbinit -config configs/dbinit.json
```

`configs/dbinit.json`：

- `dsn`：MySQL 连接串
- `file`：SQL 脚本路径

## 12. 常见问题排查

1. 端口被占用
- 命令：`netstat -ano | findstr :<port>`

2. 登录成功但大厅无实时刷新
- 检查 `gateway` 是否启动
- 检查前端是否连到了正确的 `gateway_ws_addr`

3. 能进房间但无法开始
- 检查人数是否至少 2 人
- 检查 `game` 是否可达，`/api/lobby/start` 返回是否包含 `game_addr/game_ticket`

4. 游戏中断线后状态异常
- 检查 Game WS 心跳是否持续发送（前端每 2s）
- 检查服务端超时阈值配置（`game.json`）
