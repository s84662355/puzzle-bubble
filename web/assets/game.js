const roomID = localStorage.getItem("current_room_id") || "";
const playerID = localStorage.getItem("player_id") || "";
const token = localStorage.getItem("token") || "";
const gameAddr = localStorage.getItem("game_addr") || "";
const gameTicket = localStorage.getItem("game_ticket") || "";
if (!roomID || !playerID || !token) {
  location.href = "/lobby";
}
if (!gameAddr || !gameTicket) {
  location.href = "/room";
}
function backToLobbyNow() {
  localStorage.removeItem("current_room_id");
  localStorage.removeItem("game_ticket");
  localStorage.removeItem("game_addr");
  manualClose = true;
  if (hbTimer) {
    clearInterval(hbTimer);
    hbTimer = null;
  }
  if (ws) ws.close();
  location.href = "/lobby";
}

const meta = document.getElementById("gameMeta");
const stateText = document.getElementById("stateText");
const scoreText = document.getElementById("scoreText");
const othersWrap = document.getElementById("othersWrap");
const canvas = document.getElementById("gameCanvas");
const ctx = canvas.getContext("2d");

meta.textContent = `Room: ${roomID} | Player: ${playerID}`;
document.getElementById("backRoomBtn").onclick = () => {
  leaveRoomAndGoLobby();
};

const W = canvas.width;
const H = canvas.height;
const R = 16;
const DIAM = R * 2;
const ROW_GAP = 28;
const SHOOTER_Y = H - 72;
const BOARD_ROWS = 14;
const LOSE_ROW = BOARD_ROWS - 1;
const DANGER_LINE_Y = calcDangerLineY(R);
const COLORS = ["#f25f5c", "#247ba0", "#70c1b3", "#ffe066", "#9b5de5", "#00bbf9"];
const MIN_A = -Math.PI + 0.2;
const MAX_A = -0.2;

let aimAngle = -Math.PI / 2;
let selfState = null;
let others = [];
let ws = null;
let wsReady = false;
let hbTimer = null;
let lastAimSent = 0;
let manualClose = false;
let localMoving = null;
let pendingLanding = false;
let lastFrameTS = 0;
const remoteTracks = new Map();

canvas.addEventListener("mousemove", onAim);
canvas.addEventListener("click", onShoot);
canvas.addEventListener("touchstart", (e) => {
  const t = e.changedTouches[0];
  onAimWithPoint(t.clientX, t.clientY);
  onShoot();
  e.preventDefault();
}, { passive: false });

function onAim(e) {
  onAimWithPoint(e.clientX, e.clientY);
}

function onAimWithPoint(clientX, clientY) {
  const rect = canvas.getBoundingClientRect();
  const x = clientX - rect.left;
  const y = clientY - rect.top;
  const dx = x - W / 2;
  const dy = y - SHOOTER_Y;
  const a = Math.atan2(dy, dx);
  aimAngle = Math.max(MIN_A, Math.min(MAX_A, a));
  const now = Date.now();
  if (wsReady && ws && now - lastAimSent > 40) {
    ws.send(JSON.stringify({ type: "aim", angle: aimAngle }));
    lastAimSent = now;
  }
}

async function onShoot() {
  if (!selfState) return;
  if (selfState.state !== "playing") return;
  if (selfState.moving || selfState.pending_shot || localMoving || pendingLanding) return;
  if (!wsReady || !ws) {
    stateText.textContent = "sync disconnected";
    return;
  }
  const board = Array.isArray(selfState.board) ? selfState.board : [];
  const hit = simulateLanding(board, aimAngle);
  if (!hit) {
    stateText.textContent = "landing calc failed";
    return;
  }
  ws.send(JSON.stringify({ type: "shot", angle: aimAngle }));
  beginLocalShot(aimAngle, hit.x, hit.y);
}

function connectWS() {
  const proto = location.protocol === "https:" ? "wss" : "ws";
  let host = gameAddr.trim();
  if (host.startsWith("ws://") || host.startsWith("wss://") || host.startsWith("http://") || host.startsWith("https://")) {
    host = host.replace(/^https?:\/\//, "").replace(/^wss?:\/\//, "");
  }
  const url = `${proto}://${host}/ws/room?room_id=${encodeURIComponent(roomID)}&player_id=${encodeURIComponent(playerID)}&ticket=${encodeURIComponent(gameTicket)}&token=${encodeURIComponent(token)}`;
  ws = new WebSocket(url);
  ws.onopen = () => {
    wsReady = true;
    stateText.textContent = "Connected";
    if (hbTimer) clearInterval(hbTimer);
    hbTimer = setInterval(() => {
      if (wsReady && ws) {
        ws.send(JSON.stringify({ type: "heartbeat" }));
      }
    }, 2000);
  };
  ws.onmessage = (ev) => {
    try {
      const data = JSON.parse(ev.data || "{}");
      if (data.type === "state") {
        selfState = data.self || null;
        others = data.others || [];
        if (selfState && !selfState.moving) {
          pendingLanding = false;
        }
        if (data.room && data.room.state && data.room.state !== "playing") {
          const wid = data.room.winner_id || "";
          if (wid) {
            stateText.textContent = wid === playerID ? "You win" : `You lose, winner: ${wid}`;
          }
          manualClose = true;
          if (ws) ws.close();
          setTimeout(() => { location.href = "/room"; }, 300);
          return;
        }
        renderOthers();
      } else if (data.type === "game_over") {
        const wid = data.winner_id || "";
        stateText.textContent = (wid === playerID || data.result === "win")
          ? "You win"
          : `You lose${wid ? `, winner: ${wid}` : ""}`;
        manualClose = true;
        if (ws) ws.close();
        setTimeout(() => { location.href = "/room"; }, 300);
        return;
      } else if (data.type === "game_stopped") {
        stateText.textContent = "Game stopped: player left";
        manualClose = true;
        if (ws) ws.close();
        location.href = "/room";
        return;
      } else if (data.type === "error") {
        const msg = data.msg || "server error";
        stateText.textContent = msg;
        if (msg.includes("invalid session or ticket")) {
          manualClose = true;
          if (ws) ws.close();
          location.href = "/room";
          return;
        }
        if (msg.includes("player not in room") || msg.includes("room closed") || msg.includes("room not found")) {
          backToLobbyNow();
          return;
        }
      }
    } catch {
      // ignore
    }
  };
  ws.onclose = () => {
    wsReady = false;
    if (hbTimer) {
      clearInterval(hbTimer);
      hbTimer = null;
    }
    localMoving = null;
    pendingLanding = false;
    if (manualClose) return;
    stateText.textContent = "Disconnected, reconnecting...";
    setTimeout(connectWS, 1000);
  };
  ws.onerror = () => {
    wsReady = false;
  };
}

window.addEventListener("beforeunload", () => {
  manualClose = true;
  if (hbTimer) {
    clearInterval(hbTimer);
    hbTimer = null;
  }
  if (ws) ws.close();
});

function draw(ts) {
  advanceLocalShot(ts || performance.now());
  ctx.clearRect(0, 0, W, H);
  drawBoardBackground(ctx);
  drawShooterBase(ctx);
  drawDangerLine(ctx, W, DANGER_LINE_Y);

  if (!selfState) {
    requestAnimationFrame(draw);
    return;
  }

  drawBoard(ctx, selfState.board, W, H, R);
  if (localMoving) {
    drawBubble(ctx, localMoving.x, localMoving.y, COLORS[localMoving.color % COLORS.length], R);
  } else if (selfState.moving) {
    drawBubble(ctx, selfState.moving.x, selfState.moving.y, COLORS[selfState.moving.color % COLORS.length], R);
  }
  drawAimLine();

  if (!selfState.moving && !localMoving && !pendingLanding) {
    drawBubble(ctx, W / 2, SHOOTER_Y, COLORS[selfState.current_color % COLORS.length], R);
  }
  drawBubble(ctx, W - 36, H - 36, COLORS[selfState.next_color % COLORS.length], 11);

  scoreText.textContent = `Score: ${selfState.score || 0}`;
  if (selfState.state !== "playing") {
    stateText.textContent = selfState.state;
  } else if (localMoving || selfState.moving) {
    stateText.textContent = "Bubble flying...";
  } else if (pendingLanding) {
    stateText.textContent = "Syncing landing...";
  } else {
    stateText.textContent = "Aiming...";
  }

  requestAnimationFrame(draw);
}

function drawAimLine() {
  const a = selfState && typeof selfState.aim_angle === "number" ? selfState.aim_angle : aimAngle;
  const len = 92;
  const ax = W / 2 + Math.cos(a) * len;
  const ay = SHOOTER_Y + Math.sin(a) * len;
  ctx.strokeStyle = "rgba(180,240,255,0.95)";
  ctx.lineWidth = 2.6;
  ctx.beginPath();
  ctx.moveTo(W / 2, SHOOTER_Y);
  ctx.lineTo(ax, ay);
  ctx.stroke();
  ctx.strokeStyle = "rgba(255,255,255,0.5)";
  ctx.lineWidth = 1.2;
  ctx.setLineDash([4, 5]);
  ctx.beginPath();
  ctx.moveTo(W / 2, SHOOTER_Y);
  ctx.lineTo(W / 2 + Math.cos(a) * 132, SHOOTER_Y + Math.sin(a) * 132);
  ctx.stroke();
  ctx.setLineDash([]);
}

function drawBoardBackground(context) {
  const bg = context.createLinearGradient(0, 0, 0, H);
  bg.addColorStop(0, "#123761");
  bg.addColorStop(0.65, "#1c2c56");
  bg.addColorStop(1, "#2a2552");
  context.fillStyle = bg;
  context.fillRect(0, 0, W, H);

  context.save();
  context.globalAlpha = 0.22;
  context.fillStyle = "#d4f3ff";
  for (let i = 0; i < 18; i++) {
    const sx = (i * 71) % W;
    const sy = (i * 97) % Math.floor(H * 0.72);
    context.beginPath();
    context.arc(sx + 10, sy + 8, 1.5 + (i % 3) * 0.5, 0, Math.PI * 2);
    context.fill();
  }
  context.restore();

  context.fillStyle = "rgba(255,255,255,0.06)";
  context.fillRect(0, 0, W, ROW_GAP * BOARD_ROWS + R);
}

function drawShooterBase(context) {
  const gx = context.createRadialGradient(W / 2, SHOOTER_Y + 6, 6, W / 2, SHOOTER_Y + 6, 42);
  gx.addColorStop(0, "rgba(125,244,255,0.55)");
  gx.addColorStop(1, "rgba(125,244,255,0)");
  context.fillStyle = gx;
  context.beginPath();
  context.arc(W / 2, SHOOTER_Y + 6, 42, 0, Math.PI * 2);
  context.fill();
}

function drawDangerLine(context, width, y) {
  context.strokeStyle = "rgba(255,87,87,0.9)";
  context.lineWidth = 2;
  context.setLineDash([8, 6]);
  context.beginPath();
  context.moveTo(0, y);
  context.lineTo(width, y);
  context.stroke();
  context.setLineDash([]);
}

function calcDangerLineY(radius) {
  const rowGap = Math.round(Math.sqrt(3) * radius);
  return radius + LOSE_ROW * rowGap + radius * 0.8;
}

function drawBoard(context, board, width, height, radius) {
  if (!Array.isArray(board)) return;
  const diam = radius * 2;
  const rowGap = Math.round(Math.sqrt(3) * radius);
  for (let r = 0; r < board.length; r++) {
    const row = board[r];
    if (!Array.isArray(row)) continue;
    const shift = r % 2 ? radius : 0;
    for (let c = 0; c < row.length; c++) {
      const val = row[c];
      if (typeof val !== "number" || val < 0) continue;
      const x = radius + shift + c * diam;
      const y = radius + r * rowGap;
      drawBubble(context, x, y, COLORS[val % COLORS.length], radius);
    }
  }
}

function drawBubble(context, x, y, color, radius) {
  context.beginPath();
  context.arc(x, y, radius, 0, Math.PI * 2);
  context.fillStyle = color;
  context.fill();
  context.lineWidth = Math.max(1, radius * 0.15);
  context.strokeStyle = "rgba(255,255,255,0.35)";
  context.stroke();
  const g = context.createRadialGradient(x - radius * 0.34, y - radius * 0.38, 1, x, y, radius * 1.05);
  g.addColorStop(0, "rgba(255,255,255,0.55)");
  g.addColorStop(1, "rgba(255,255,255,0)");
  context.fillStyle = g;
  context.fill();
  context.beginPath();
  context.arc(x - radius * 0.32, y - radius * 0.36, radius * 0.22, 0, Math.PI * 2);
  context.fillStyle = "rgba(255,255,255,0.45)";
  context.fill();
}

function renderOthers() {
  if (!others.length) {
    othersWrap.innerHTML = "<p class='hint'>Waiting for other players...</p>";
    return;
  }
  othersWrap.innerHTML = "";
  for (const item of others) {
    const st = item.state || {};
    updateRemoteTrack(item.player_id, st);
    const card = document.createElement("div");
    card.className = "mini-card";

    const head = document.createElement("div");
    head.className = "mini-head";
    head.textContent = `${item.player_id} | ${st.state || "playing"} | ${st.score || 0}`;
    card.appendChild(head);

    const mini = document.createElement("canvas");
    mini.className = "mini-canvas";
    mini.width = 160;
    mini.height = 200;
    card.appendChild(mini);

    const c = mini.getContext("2d");
    c.fillStyle = "#0f2534";
    c.fillRect(0, 0, mini.width, mini.height);
    drawDangerLine(c, mini.width, calcDangerLineY(5));
    drawBoard(c, st.board || [], mini.width, mini.height, 5);
    if (st.moving) {
      drawBubble(c, st.moving.x * (160 / 560), st.moving.y * (200 / 760), COLORS[st.moving.color % COLORS.length], 5);
      c.strokeStyle = "rgba(255,255,255,0.35)";
      c.beginPath();
      c.moveTo(st.moving.x * (160 / 560), st.moving.y * (200 / 760));
      c.lineTo((st.moving.x + st.moving.vx * 0.1) * (160 / 560), (st.moving.y + st.moving.vy * 0.1) * (200 / 760));
      c.stroke();
    } else {
      const p = getRemoteTrackPoint(item.player_id, Date.now());
      if (p) {
        drawBubble(c, p.x * (160 / 560), p.y * (200 / 760), COLORS[p.color % COLORS.length], 5);
        c.strokeStyle = "rgba(255,255,255,0.35)";
        c.beginPath();
        c.moveTo(p.x * (160 / 560), p.y * (200 / 760));
        c.lineTo((p.x + p.vx * 0.08) * (160 / 560), (p.y + p.vy * 0.08) * (200 / 760));
        c.stroke();
      }
    }
    const oa = typeof st.aim_angle === "number" ? st.aim_angle : -Math.PI / 2;
    const sx = 160 / 2;
    const sy = 200 - 20;
    c.strokeStyle = "rgba(255,255,255,0.45)";
    c.beginPath();
    c.moveTo(sx, sy);
    c.lineTo(sx + Math.cos(oa) * 22, sy + Math.sin(oa) * 22);
    c.stroke();

    othersWrap.appendChild(card);
  }
}

function updateRemoteTrack(playerId, st) {
  if (!st || st.pending_shot !== true) {
    remoteTracks.delete(playerId);
    return;
  }
  const seq = Number(st.shot_seq || 0);
  const old = remoteTracks.get(playerId);
  if (old && old.seq === seq) return;
  const angle = Number(st.shot_angle || -Math.PI / 2);
  const color = Number(st.shot_color || 0);
  const at = Number(st.shot_at_ms || Date.now());
  remoteTracks.set(playerId, {
    seq,
    at,
    angle,
    color,
  });
}

function getRemoteTrackPoint(playerId, nowMS) {
  const t = remoteTracks.get(playerId);
  if (!t) return null;
  let x = W / 2;
  let y = SHOOTER_Y;
  let vx = Math.cos(t.angle) * 520;
  let vy = Math.sin(t.angle) * 520;
  let dt = Math.max(0, Math.min(2.2, (nowMS - t.at) / 1000));
  const step = 1 / 120;
  while (dt > 0) {
    const s = Math.min(step, dt);
    x += vx * s;
    y += vy * s;
    if (x <= R) {
      x = R;
      vx = Math.abs(vx);
    } else if (x >= W - R) {
      x = W - R;
      vx = -Math.abs(vx);
    }
    if (y <= R) {
      y = R;
      break;
    }
    dt -= s;
  }
  return { x, y, vx, vy, color: t.color };
}

connectWS();
requestAnimationFrame(draw);
validateRoomMembership();
setInterval(validateRoomMembership, 3000);

async function leaveRoomAndGoLobby() {
  try {
    const resp = await fetch("/api/lobby/leave", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-Player-Id": playerID,
      },
      body: JSON.stringify({ room_id: roomID }),
    });
    const data = await resp.json();
    manualClose = true;
    if (ws) ws.close();
    location.href = (data && data.next_page) || "/lobby";
    return;
  } catch {
    manualClose = true;
    if (ws) ws.close();
    location.href = "/lobby";
  }
}

async function validateRoomMembership() {
  try {
    const resp = await fetch(`/api/lobby/room?room_id=${encodeURIComponent(roomID)}`, {
      headers: { "X-Player-Id": playerID },
    });
    if (!resp.ok) {
      backToLobbyNow();
      return;
    }
  } catch {
    // 网络短暂抖动时保持当前页面
  }
}
function beginLocalShot(angle, hitX, hitY) {
  localMoving = {
    x: W / 2,
    y: SHOOTER_Y,
    vx: Math.cos(angle) * 520,
    vy: Math.sin(angle) * 520,
    color: selfState.current_color,
    hitX,
    hitY,
    angle,
  };
}

function advanceLocalShot(ts) {
  if (!localMoving) {
    lastFrameTS = ts;
    return;
  }
  const dt = Math.min(0.033, Math.max(0, (ts - lastFrameTS) / 1000));
  lastFrameTS = ts;
  localMoving.x += localMoving.vx * dt;
  localMoving.y += localMoving.vy * dt;

  if (localMoving.x <= R) {
    localMoving.x = R;
    localMoving.vx = Math.abs(localMoving.vx);
  } else if (localMoving.x >= W - R) {
    localMoving.x = W - R;
    localMoving.vx = -Math.abs(localMoving.vx);
  }

  const dx = localMoving.x - localMoving.hitX;
  const dy = localMoving.y - localMoving.hitY;
  if (dx * dx + dy * dy <= 14 * 14) {
    if (!wsReady || !ws) {
      localMoving = null;
      pendingLanding = false;
      return;
    }
    pendingLanding = true;
    ws.send(JSON.stringify({ type: "land", angle: localMoving.angle, x: localMoving.hitX, y: localMoving.hitY }));
    localMoving = null;
  }
}

function simulateLanding(board, angle) {
  let x = W / 2;
  let y = SHOOTER_Y;
  let vx = Math.cos(angle) * 520;
  let vy = Math.sin(angle) * 520;
  const dt = 1 / 120;
  for (let i = 0; i < 5000; i++) {
    x += vx * dt;
    y += vy * dt;
    if (x <= R) {
      x = R;
      vx = Math.abs(vx);
    } else if (x >= W - R) {
      x = W - R;
      vx = -Math.abs(vx);
    }
    if (y <= R || collideBoard(board, x, y)) {
      return { x, y };
    }
  }
  return null;
}

function collideBoard(board, x, y) {
  if (!Array.isArray(board)) return false;
  for (let r = 0; r < board.length; r++) {
    const row = board[r];
    if (!Array.isArray(row)) continue;
    const shift = r % 2 ? R : 0;
    for (let c = 0; c < row.length; c++) {
      if ((row[c] | 0) < 0) continue;
      const cx = R + shift + c * DIAM;
      const cy = R + r * ROW_GAP;
      const dx = x - cx;
      const dy = y - cy;
      if (dx * dx + dy * dy <= (DIAM - 2) * (DIAM - 2)) {
        return true;
      }
    }
  }
  return false;
}





