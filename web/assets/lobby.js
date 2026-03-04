const playerID = localStorage.getItem("player_id") || "";
const token = localStorage.getItem("token") || "";
const playerName = document.getElementById("playerName");
const roomList = document.getElementById("roomList");
const msgBox = document.getElementById("msgBox");
let lobbyWS = null;
let reloadTimer = null;

if (!playerID || !token) {
  location.href = "/";
}
playerName.textContent = playerID;

document.getElementById("logoutBtn").onclick = () => {
  localStorage.removeItem("player_id");
  localStorage.removeItem("token");
  localStorage.removeItem("current_room_id");
  location.href = "/";
};

document.getElementById("refreshBtn").onclick = loadRooms;
document.getElementById("createBtn").onclick = createRoom;

async function loadRooms() {
  msgBox.textContent = "";
  await syncCurrentRoomMembership();
  const resp = await fetch("/api/lobby/rooms");
  const data = await resp.json();
  if (data.code !== 0) {
    msgBox.textContent = "拉取房间失败";
    return;
  }
  const rooms = data.rooms || [];
  if (!rooms.length) {
    roomList.innerHTML = "<p class='hint'>暂无房间，先创建一个吧。</p>";
    return;
  }
  const currentRoomID = localStorage.getItem("current_room_id") || "";
  roomList.innerHTML = rooms.map((r) => {
    const joined = r.room_id === currentRoomID;
    return `
      <div class="room-item">
        <div>
          <strong>${escapeHTML(r.name)}</strong>
          <div class="room-meta">
            房间ID: ${r.room_id} | 房主: ${r.owner_id} | 人数: ${r.player_cnt}/${r.max_players}
          </div>
        </div>
        ${joined
          ? "<button class='btn mini ghost' disabled>已在房间</button>"
          : `<button class="btn mini" onclick="joinRoom('${r.room_id}')">加入</button>`}
      </div>
    `;
  }).join("");
}

async function syncCurrentRoomMembership() {
  const currentRoomID = localStorage.getItem("current_room_id") || "";
  if (!currentRoomID) return;
  try {
    const resp = await fetch(`/api/lobby/room?room_id=${encodeURIComponent(currentRoomID)}`, {
      headers: { "X-Player-Id": playerID },
    });
    if (!resp.ok) {
      clearRoomLocalState();
    }
  } catch {
    // 网络波动时不清理，避免误判
  }
}

function clearRoomLocalState() {
  localStorage.removeItem("current_room_id");
  localStorage.removeItem("game_addr");
  localStorage.removeItem("game_ticket");
}

async function createRoom() {
  msgBox.textContent = "";
  const name = document.getElementById("roomName").value.trim();
  const maxPlayers = Number(document.getElementById("maxPlayers").value || 2);
  const resp = await fetch("/api/lobby/rooms", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-Player-Id": playerID,
    },
    body: JSON.stringify({ name, max_players: maxPlayers }),
  });
  const data = await readMaybeJSON(resp);
  if (!resp.ok) {
    msgBox.textContent = normalizeError(data);
    return;
  }

  localStorage.setItem("current_room_id", data.room.room_id);
  msgBox.textContent = "创建成功并已加入房间: " + data.room.room_id;
  location.href = "/room";
}

window.joinRoom = async function joinRoom(roomID) {
  msgBox.textContent = "";
  const resp = await fetch("/api/lobby/join", {
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
  localStorage.setItem("current_room_id", roomID);
  if (data.msg === "already_in_room") {
    msgBox.textContent = "你已在该房间";
  } else {
    msgBox.textContent = "加入成功: " + roomID;
  }
  location.href = "/room";
};

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
  if (!data) return "操作失败";
  if (typeof data === "string") return data;
  if (data.raw) return String(data.raw);
  if (data.msg) return String(data.msg);
  return JSON.stringify(data);
}

function scheduleReload() {
  if (reloadTimer) {
    clearTimeout(reloadTimer);
  }
  reloadTimer = setTimeout(() => {
    loadRooms().catch(() => {});
  }, 120);
}

function connectLobbyWS() {
  const proto = location.protocol === "https:" ? "wss" : "ws";
  const wsHost = localStorage.getItem("gateway_ws_addr") || location.host;
  lobbyWS = new WebSocket(`${proto}://${wsHost}/ws/lobby?token=${encodeURIComponent(token)}`);
  lobbyWS.onopen = () => {
    try {
      lobbyWS.send(JSON.stringify({ type: "heartbeat" }));
    } catch {}
  };
  lobbyWS.onmessage = (ev) => {
    try {
      const data = JSON.parse(ev.data || "{}");
      if (data.type === "lobby_rooms_updated") {
        scheduleReload();
      }
    } catch {}
  };
  lobbyWS.onclose = () => {
    setTimeout(connectLobbyWS, 1000);
  };
}

window.addEventListener("beforeunload", () => {
  if (lobbyWS) {
    lobbyWS.close();
  }
});

loadRooms();
connectLobbyWS();
