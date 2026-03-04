const form = document.getElementById("loginForm");
const errBox = document.getElementById("errBox");

form.addEventListener("submit", async (e) => {
  e.preventDefault();
  errBox.textContent = "";
  const username = document.getElementById("username").value.trim();
  const password = document.getElementById("password").value.trim();
  if (!username || !password) {
    errBox.textContent = "请输入账号和密码";
    return;
  }

  try {
    const resp = await fetch("/api/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username, password }),
    });
    const data = await resp.json();
    if (!resp.ok) {
      errBox.textContent = JSON.stringify(data);
      return;
    }
    localStorage.setItem("token", data.token || "");
    localStorage.setItem("player_id", data.player_id || username);
    location.href = "/lobby";
  } catch (err) {
    errBox.textContent = "登录失败: " + String(err);
  }
});
