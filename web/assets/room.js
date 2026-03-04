const roomID = localStorage.getItem("current_room_id") || "";
const playerID = localStorage.getItem("player_id") || "";
const token = localStorage.getItem("token") || "";

if (!roomID || !playerID || !token) {
  location.href = "/lobby";
}
function backToLobbyNow() {
  localStorage.removeItem("current_room_id");
  localStorage.removeItem("game_ticket");
  localStorage.removeItem("game_addr");
  stopRoomHeartbeatWS();
  if (eventSource) {
    eventSource.close();
    eventSource = null;
  }
  location.href = "/lobby";
}

const roomTitle = document.getElementById("roomTitle");
const roomMeta = document.getElementById("roomMeta");
const memberList = document.getElementById("memberList");
const startBtn = document.getElementById("startBtn");
const msgBox = document.getElementById("msgBox");
let eventSource = null;
let roomWS = null;
let hbTimer = null;
let roomWSManualClose = false;

document.getElementById("backLobbyBtn").onclick = () => {
  leaveRoomAndGo("/lobby");
};

startBtn.onclick = async () => {
  msgBox.textContent = "";
  const resp = await fetch("/api/lobby/start", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-Player-Id": playerID,
    },
    body: JSON.stringify({ room_id: roomID }),
  });
  const data = await readMaybeJSON(resp);
  if (!resp.ok) {
    msgBox.textContent = normalizeError(data);
    return;
  }
  if (data && data.game_addr) {
    localStorage.setItem("game_addr", data.game_addr);
  }
  if (data && data.game_ticket) {
    localStorage.setItem("game_ticket", data.game_ticket);
  }
  if (!data || !data.game_addr || !data.game_ticket) {
    msgBox.textContent = "game session not ready";
    return;
  }
  msgBox.textContent = "Game started";
  toGamePage();
};

async function loadRoom() {
  const resp = await fetch(`/api/lobby/room?room_id=${encodeURIComponent(roomID)}`, {
    headers: { "X-Player-Id": playerID },
  });
  if (!resp.ok) {
    backToLobbyNow();
    return;
  }

  const data = await resp.json();
  const room = data.room || {};
  const members = data.members || [];
  const isOwner = !!data.is_owner;
  const state = room.state || "waiting";
  if (data.game_addr) {
    localStorage.setItem("game_addr", data.game_addr);
  }
  if (data.game_ticket) {
    localStorage.setItem("game_ticket", data.game_ticket);
  }
  if (state === "playing") {
    if (!data.game_addr || !data.game_ticket) {
      roomMeta.textContent = "Game is starting, waiting for session ticket...";
      return;
    }
    toGamePage();
    return;
  }

  roomTitle.textContent = `Room ${room.room_id || "-"}`;
  roomMeta.textContent = `Owner: ${room.owner_id || "-"} | Players: ${room.player_cnt || 0}/${room.max_players || 0} | State: ${state}`;

  if (isOwner && state === "waiting") {
    startBtn.style.display = "inline-block";
  } else {
    startBtn.style.display = "none";
  }

  if (!members.length) {
    memberList.innerHTML = "<p class='hint'>No members</p>";
  } else {
    memberList.innerHTML = members.map((m) => `<div class="room-item"><strong>${escapeHTML(m)}</strong></div>`).join("");
  }
}

function connectRoomEvents() {
  if (eventSource) return;
  const url = `/api/lobby/room/events?room_id=${encodeURIComponent(roomID)}&player_id=${encodeURIComponent(playerID)}`;
  eventSource = new EventSource(url);
  eventSource.addEventListener("room", (ev) => {
    try {
      const data = JSON.parse(ev.data || "{}");
      if (data.type === "game_started") {
        loadRoom();
        return;
      }
      if (data.type === "room_deleted") {
        backToLobbyNow();
        return;
      }
      loadRoom();
    } catch {
      // ignore malformed events
    }
  });
  eventSource.onerror = () => {
    // keep polling
  };
}

function toGamePage() {
  stopRoomHeartbeatWS();
  if (eventSource) {
    eventSource.close();
    eventSource = null;
  }
  location.href = "/game";
}

function escapeHTML(s) {
  return String(s)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}

async function readMaybeJSON(resp) {
  const text = await resp.text();
  try {
    return JSON.parse(text);
  } catch {
    return { raw: text };
  }
}

function normalizeError(data) {
  if (!data) return "request failed";
  if (typeof data === "string") return data;
  if (data.raw) return String(data.raw);
  if (data.msg) return String(data.msg);
  return JSON.stringify(data);
}

loadRoom();
connectRoomEvents();
connectRoomHeartbeatWS();
setInterval(loadRoom, 3000);

async function leaveRoomAndGo(fallback) {
  stopRoomHeartbeatWS();
  try {
    const resp = await fetch("/api/lobby/leave", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-Player-Id": playerID,
      },
      body: JSON.stringify({ room_id: roomID }),
    });
    const data = await readMaybeJSON(resp);
    if (data && data.next_page) {
      location.href = data.next_page;
      return;
    }
  } catch {
    // ignore network error and fallback
  }
  location.href = fallback;
}

function connectRoomHeartbeatWS() {
  const proto = location.protocol === "https:" ? "wss" : "ws";
  const host = localStorage.getItem("game_addr") || `${location.hostname}:19500`;
  const url = `${proto}://${host}/ws/room?room_id=${encodeURIComponent(roomID)}&player_id=${encodeURIComponent(playerID)}&token=${encodeURIComponent(token)}`;
  roomWS = new WebSocket(url);
  roomWS.onopen = () => {
    if (hbTimer) clearInterval(hbTimer);
    hbTimer = setInterval(() => {
      if (roomWS && roomWS.readyState === WebSocket.OPEN) {
        roomWS.send(JSON.stringify({ type: "heartbeat", ts: Date.now() }));
      }
    }, 2000);
  };
  roomWS.onclose = () => {
    if (hbTimer) {
      clearInterval(hbTimer);
      hbTimer = null;
    }
    if (roomWSManualClose) return;
    setTimeout(connectRoomHeartbeatWS, 1000);
  };
}

function stopRoomHeartbeatWS() {
  roomWSManualClose = true;
  if (hbTimer) {
    clearInterval(hbTimer);
    hbTimer = null;
  }
  if (roomWS) {
    roomWS.close();
    roomWS = null;
  }
}

window.addEventListener("beforeunload", () => {
  stopRoomHeartbeatWS();
});
