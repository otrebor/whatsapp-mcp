// Live pairing page: polls the bridge and always shows the current QR code.
//
// The page itself holds no secrets. The bearer token arrives in the URL
// fragment (#token=...), which browsers never send to the server; it is moved
// to sessionStorage (this tab only) and stripped from the address bar.
(() => {
  "use strict";

  const TOKEN_KEY = "whatsapp-bridge-token";
  const POLL_MS = 2000;

  const $ = (id) => document.getElementById(id);
  const statusEl = $("status");
  const detailEl = $("detail");
  const qrBox = $("qr-box");
  const qrImg = $("qr");
  const restartBtn = $("restart");
  const unlinkBtn = $("unlink");
  const tokenForm = $("token-form");
  const tokenInput = $("token");

  let token = readToken();
  let shownSeq = null;
  let shownURL = null;
  let timer = null;

  function readToken() {
    const fromHash = new URLSearchParams(location.hash.slice(1)).get("token");
    if (location.hash) {
      history.replaceState(null, "", location.pathname);
    }
    try {
      if (fromHash) {
        sessionStorage.setItem(TOKEN_KEY, fromHash);
        return fromHash;
      }
      return sessionStorage.getItem(TOKEN_KEY);
    } catch {
      return fromHash; // storage blocked: works until reload
    }
  }

  function saveToken(value) {
    token = value;
    try {
      sessionStorage.setItem(TOKEN_KEY, value);
    } catch {
      // keep it in memory only
    }
  }

  function api(path, init = {}) {
    return fetch("/api" + path, {
      ...init,
      cache: "no-store",
      headers: { Authorization: "Bearer " + token },
    });
  }

  function setStatus(text, tone, detail = "") {
    statusEl.textContent = text;
    statusEl.dataset.tone = tone || "";
    detailEl.textContent = detail;
  }

  function hideQR() {
    qrBox.hidden = true;
    shownSeq = null;
    if (shownURL) {
      URL.revokeObjectURL(shownURL);
      shownURL = null;
    }
  }

  function askForToken(message) {
    hideQR();
    restartBtn.hidden = true;
    unlinkBtn.hidden = true;
    tokenForm.hidden = false;
    setStatus(message, "warn");
    tokenInput.focus();
  }

  async function refreshQR() {
    const resp = await api("/pairing/qr.png");
    if (resp.status !== 200) {
      return false;
    }
    const seq = resp.headers.get("X-Pairing-Seq");
    if (seq !== shownSeq) {
      const url = URL.createObjectURL(await resp.blob());
      qrImg.src = url;
      if (shownURL) {
        URL.revokeObjectURL(shownURL);
      }
      shownURL = url;
      shownSeq = seq;
    }
    qrBox.hidden = false;
    return true;
  }

  async function tick() {
    timer = null;
    if (!token) {
      askForToken("Enter the bridge token to continue.");
      return;
    }

    let health;
    try {
      const resp = await api("/health");
      if (resp.status === 401) {
        askForToken("The bridge rejected the token.");
        return;
      }
      health = await resp.json();
    } catch {
      hideQR();
      restartBtn.hidden = true;
      unlinkBtn.hidden = true;
      setStatus("Bridge not reachable", "error", "Is the bridge running? Retrying…");
      schedule();
      return;
    }

    restartBtn.hidden = true;
    // Unlinking sends a request over the live connection, so only offer it
    // while connected.
    unlinkBtn.hidden = !health.connected;
    if (health.connected) {
      hideQR();
      setStatus("✓ Linked", "ok", "WhatsApp is connected. You can close this page.");
    } else {
      switch (health.pairing_state) {
        case "awaiting_scan":
          if (await refreshQR()) {
            setStatus("Scan with WhatsApp", "", "Settings › Linked devices › Link a device. The code refreshes by itself.");
          } else {
            setStatus("Waiting for a code…", "");
          }
          break;
        case "timed_out":
          hideQR();
          restartBtn.hidden = false;
          setStatus("Code expired", "warn", "No code was scanned in time.");
          break;
        case "paired":
          hideQR();
          setStatus("Linked, connecting…", "ok", "The bridge reconnects by itself.");
          break;
        default: // starting, logged_out
          hideQR();
          setStatus("Preparing a new code…", "", health.pairing_error || "");
      }
    }
    schedule();
  }

  function schedule() {
    if (timer === null) {
      timer = setTimeout(tick, POLL_MS);
    }
  }

  restartBtn.addEventListener("click", async () => {
    restartBtn.disabled = true;
    try {
      await api("/pairing/start", { method: "POST" });
    } catch {
      // the next tick reports the bridge state
    }
    restartBtn.disabled = false;
    restartBtn.hidden = true;
    setStatus("Preparing a new code…", "");
    clearTimeout(timer);
    timer = null;
    tick();
  });

  unlinkBtn.addEventListener("click", async () => {
    const ok = confirm(
      "Unlink this device from your WhatsApp account?\n\n" +
        "Sending stops until you scan a new code. Message history on this computer is kept.",
    );
    if (!ok) {
      return;
    }
    unlinkBtn.disabled = true;
    let message = "";
    try {
      const resp = await api("/logout", { method: "POST" });
      const body = await resp.json().catch(() => ({}));
      if (resp.ok) {
        unlinkBtn.hidden = true;
        setStatus("Unlinked", "ok", "Preparing a new code…");
      } else {
        message = body.message || "HTTP " + resp.status;
      }
    } catch {
      message = "bridge not reachable";
    }
    unlinkBtn.disabled = false;
    if (message) {
      setStatus("Could not unlink", "error", message);
    }
    clearTimeout(timer);
    timer = null;
    schedule();
  });

  tokenForm.addEventListener("submit", (event) => {
    event.preventDefault();
    const value = tokenInput.value.trim();
    if (!value) {
      return;
    }
    saveToken(value);
    tokenInput.value = "";
    tokenForm.hidden = true;
    setStatus("Loading…", "");
    tick();
  });

  tick();
})();
