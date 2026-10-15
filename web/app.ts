// @ts-nocheck: legacy UI, typing is incremental.
// Web terminal: JS forwards keystrokes and renders output; the Go client
// owns line editing. Bridge: v2vSendKeys(s) in, v2vOutput(s) out,
// v2vConfig read by the Go client at startup.
import { gateFetch } from "./pow_bridge.js";

(function () {
  "use strict";

  var VERSION = (typeof window.V2V_VERSION !== "undefined")
    ? window.V2V_VERSION
    : "dev";

  // The Go client invokes this once its input bridge is wired up; re-fit at
  // that moment so the real grid size reaches the editor before any output.
  window.v2vWasmReady = function () {
    resizeTerminal();
  };

  var term = null;

  var panel = document.getElementById("connect-panel");
  var wrap = document.getElementById("terminal-wrap");
  var statusLine = document.getElementById("status-line");
  var form = document.getElementById("connect-form");
  var serverInput = document.getElementById("server-url");
  var userInput = document.getElementById("username");
  var tripInput = document.getElementById("tripcode");
  var connectBtn = document.getElementById("connect-btn");
  var passkeyBtn = document.getElementById("passkey-btn");
  var passkeyRoleInput = document.getElementById("passkey-role");
  var passkeyRoleLabel = document.getElementById("passkey-role-label");
  var usePasskey = false;

  if (passkeyBtn) {
    passkeyBtn.addEventListener("click", function () {
      var hidden = getComputedStyle(passkeyRoleInput).display === "none";
      if (hidden) {
        passkeyRoleInput.style.display = "block";
        if (passkeyRoleLabel) passkeyRoleLabel.style.display = "block";
        passkeyRoleInput.focus();
        passkeyBtn.textContent = "🔑 Passkey (đã chọn)";
        usePasskey = true;
      } else {
        passkeyRoleInput.style.display = "none";
        if (passkeyRoleLabel) passkeyRoleLabel.style.display = "none";
        passkeyRoleInput.value = "";
        passkeyBtn.textContent = "🔑 Passkey";
        usePasskey = false;
      }
    });
  }

  // Auto-parse role:hash format only in Role input (avoid false-positive on username)
  if (passkeyRoleInput) {
    passkeyRoleInput.addEventListener("input", function () {
      var v = passkeyRoleInput.value;
      var idx = v.indexOf(":");
      if (idx > 0) {
        var rolePart = v.slice(0, idx).trim();
        if (rolePart && rolePart !== v) {
          passkeyRoleInput.value = rolePart;
          try { passkeyRoleInput.setSelectionRange(rolePart.length, rolePart.length); } catch (e) {}
        }
      }
    });
  }

  function setStatus(msg, isError) {
    statusLine.textContent = msg;
    statusLine.className = isError ? "error" : "";
  }

  // Settings panel.
  // Pre-connect surface for the desktop toggles; the live session owns
  // them after connect (/meta, /showjoin, /autoverify, /notify).
  // NOTIFY_TAGS mirrors lib/wire AllTags() parent-first; a Go test pins both.
  var NOTIFY_TAGS = [
    "system",
    "system.join", "system.leave", "system.date",
    "system.history",
    "system.history.begin", "system.history.older", "system.history.recover",
    "system.history.end", "system.history.exhausted",
    "system.pow", "system.pow.gate", "system.pow.screening",
    "system.audit",
    "system.limit", "system.envelope", "system.trip",
    "system.filter", "system.auth",
  ];
  var OPT_DEFAULTS = {
    meta: true,
    showJoin: false,
    autoVerify: true,
    notify: {},
    powMinTier: 1,
  };
  // Absent tags read as shown, so new tags are never silenced by omission.
  NOTIFY_TAGS.forEach(function (t) { OPT_DEFAULTS.notify[t] = true; });
  var OPT_KEY = "v2v.config";
  var opts = cloneOpts(OPT_DEFAULTS);

  function cloneOpts(src) {
    var notify = {};
    Object.keys(src.notify).forEach(function (k) { notify[k] = src.notify[k]; });
    return {
      meta: src.meta,
      showJoin: src.showJoin,
      autoVerify: src.autoVerify,
      notify: notify,
      powMinTier: src.powMinTier,
    };
  }

  // Tab storage survives reloads (/quit reloads) but dies with the tab;
  // the persist switch additionally mirrors to localStorage.
  function loadOpts() {
    var raw = null;
    try {
      raw = window.localStorage.getItem(OPT_KEY) || window.sessionStorage.getItem(OPT_KEY);
    } catch (e) { /* storage may be blocked; defaults stand */ }
    if (!raw) return;
    try {
      var got = JSON.parse(raw);
      if (got && typeof got === "object") {
        ["meta", "showJoin", "autoVerify"].forEach(function (k) {
          if (typeof got[k] === "boolean") opts[k] = got[k];
        });
        // Tier sits beside notify; read it outside that guard or a
        // tier-only payload falls back to 1.
        if (typeof got.powMinTier === "number" && got.powMinTier >= 1) {
          opts.powMinTier = Math.floor(got.powMinTier);
        }
        if (got.notify && typeof got.notify === "object") {
          NOTIFY_TAGS.forEach(function (k) {
            if (typeof got.notify[k] === "boolean") opts.notify[k] = got.notify[k];
          });
        }
      }
    } catch (e) { /* corrupt payload: keep defaults */ }
  }

  function persistEnabled() {
    try { return !!window.localStorage.getItem(OPT_KEY); } catch (e) { return false; }
  }

  function saveOpts() {
    var raw = JSON.stringify(opts);
    try {
      window.sessionStorage.setItem(OPT_KEY, raw);
    } catch (e) { /* ignore */ }
    if (persistEnabled()) {
      try { window.localStorage.setItem(OPT_KEY, raw); } catch (e) { /* ignore */ }
    }
  }

  function clearStoredOpts() {
    try { window.sessionStorage.removeItem(OPT_KEY); } catch (e) { /* ignore */ }
    try { window.localStorage.removeItem(OPT_KEY); } catch (e) { /* ignore */ }
  }

  function el(id) { return document.getElementById(id); }

  function syncJoinDep() {
    // The join rows only have meaning while join/leave render at all.
    ["opt-notifyjoin-row", "opt-notifyleave-row"].forEach(function (id) {
      var row = el(id);
      if (row) row.classList.toggle("dimmed", !opts.showJoin);
    });
  }

  // Row label is the leaf below its parent ("screening", not the full path).
  function notifyLeaf(tag) {
    var cut = tag.lastIndexOf(".");
    return cut < 0 ? tag : tag.slice(cut + 1);
  }

  // Generate rows from NOTIFY_TAGS so the panel cannot drift from the taxonomy.
  function buildNotifyRows() {
    var host = el("notify-tree");
    if (!host) return;
    host.textContent = "";
    NOTIFY_TAGS.forEach(function (tag) {
      var label = document.createElement("label");
      label.className = "check" + (tag.indexOf(".") < 0 ? "" : " nested");
      var input = document.createElement("input");
      input.type = "checkbox";
      input.id = "opt-notify-" + tag;
      if (tag === "system.join") label.id = "opt-notifyjoin-row";
      if (tag === "system.leave") label.id = "opt-notifyleave-row";
      input.dataset.notifyTag = tag;
      label.appendChild(input);
      label.appendChild(document.createTextNode(notifyLeaf(tag)));
      host.appendChild(label);
    });
  }

  function applyOptsToUI() {
    el("opt-meta").checked = opts.meta;
    el("opt-showjoin").checked = opts.showJoin;
    el("opt-autoverify").checked = opts.autoVerify;
    el("opt-powmintier").value = String(opts.powMinTier);
    NOTIFY_TAGS.forEach(function (tag) {
      var input = el("opt-notify-" + tag);
      // Show the effective gate: a muted parent reads its children as off.
      if (input) input.checked = notifyTagEnabled(tag);
    });
    el("opt-persist").checked = persistEnabled();
    syncJoinDep();
  }

  // Explicit [option path, input id] pairs so wiring cannot drift from markup.
  var OPT_SWITCHES = [
    ["meta", "opt-meta"],
    ["showJoin", "opt-showjoin"],
    ["autoVerify", "opt-autoverify"],
  ];

  // Mirrors client gating: a tag is on unless it or any ancestor is off.
  // Tree order evaluates parents before children.
  function notifyTagEnabled(tag) {
    // Ancestors precede the tag, so a prefix match finds them.
    for (var i = 0; i < NOTIFY_TAGS.length; i++) {
      var cur = NOTIFY_TAGS[i];
      if (cur === tag) break;
      if (tag.indexOf(cur + ".") === 0 && opts.notify[cur] === false) return false;
    }
    return opts.notify[tag] !== false;
  }

  function readOpt(path) {
    var parts = path.split(".");
    var v = opts;
    for (var i = 0; i < parts.length; i++) v = v[parts[i]];
    return v;
  }

  function writeOpt(path, value) {
    var parts = path.split(".");
    var last = parts.pop();
    var v = opts;
    for (var i = 0; i < parts.length; i++) v = v[parts[i]];
    v[last] = value;
  }

  function initConfigPanel() {
    var btn = el("config-btn");
    var panel = el("config-panel");
    if (!btn || !panel) return;
    buildNotifyRows();
    loadOpts();
    applyOptsToUI();
    btn.addEventListener("click", function () {
      var open = panel.classList.contains("hidden");
      panel.classList.toggle("hidden");
      btn.textContent = open ? "⚙️ Ẩn tuỳ chọn" : "⚙️ Tuỳ chọn";
    });
    OPT_SWITCHES.forEach(function (pair) {
      var input = el(pair[1]);
      if (!input) return;
      input.addEventListener("change", function () {
        writeOpt(pair[0], this.checked);
        if (pair[0] === "showJoin") syncJoinDep();
        saveOpts();
      });
    });
    var tier = el("opt-powmintier");
    if (tier) {
      tier.addEventListener("change", function () {
        // Empty input keeps the stored value: falling back to 1 would
        // announce every tier instead of tightening the floor.
        var n = Math.floor(Number(this.value));
        if (this.value.trim() === "" || !isFinite(n)) {
          this.value = String(readOpt("powMinTier"));
          return;
        }
        writeOpt("powMinTier", Math.min(9, Math.max(1, n)));
        this.value = String(readOpt("powMinTier"));
        saveOpts();
      });
    }
    // Re-sync the tree: children of a muted parent read as off.
    var tree = el("notify-tree");
    if (tree) {
      tree.addEventListener("change", function (ev) {
        var tag = ev.target && ev.target.dataset && ev.target.dataset.notifyTag;
        if (!tag) return;
        opts.notify[tag] = ev.target.checked;
        saveOpts();
        applyOptsToUI();
      });
    }
    var persist = el("opt-persist");
    if (persist) {
      persist.addEventListener("change", function () {
        if (this.checked) {
          try { window.localStorage.setItem(OPT_KEY, JSON.stringify(opts)); } catch (e) { /* ignore */ }
        } else {
          try { window.localStorage.removeItem(OPT_KEY); } catch (e) { /* ignore */ }
        }
      });
    }
    var reset = el("config-reset");
    if (reset) {
      reset.addEventListener("click", function () {
        clearStoredOpts();
        opts = cloneOpts(OPT_DEFAULTS);
        applyOptsToUI();
      });
    }
  }

  initConfigPanel();

  // Expose status setter so WASM shows auth errors without a reload.
  window.v2vSetStatus = function (msg, isError) {
    setStatus(msg, !!isError);
    if (isError) {
      // Reveal the login panel (hidden at boot) so the error stays visible.
      panel.style.display = "block";
      wrap.style.display = "none";
    }
    // Re-enable the connect button so the user can retry.
    if (connectBtn) connectBtn.disabled = false;
  };

  var FONT_FAMILY = '"JetBrains Mono", Menlo, Consolas, "DejaVu Sans Mono", monospace';

  // Monospace advance width ~= 0.6em, used for the initial font-size guess.
  var CHAR_ASPECT = 0.6;
  var resizeTimer = null;

  // Mirror xterm cell metrics: canvas measureText("W"), DOM probe fallback.
  function measureCell(fontSize) {
    var dpr = window.devicePixelRatio || 1;
    var letterSpacing = (term && term.options && term.options.letterSpacing) || 0;
    var lineHeight = (term && term.options && term.options.lineHeight) || 1;
    var charW = 0;
    var charH = 0;
    try {
      var canvas = document.createElement("canvas");
      var ctx = canvas.getContext("2d");
      if (ctx) {
        ctx.font = fontSize + "px " + FONT_FAMILY;
        var m = ctx.measureText("W");
        if (m && "width" in m && "fontBoundingBoxAscent" in m && "fontBoundingBoxDescent" in m) {
          charW = m.width;
          charH = m.fontBoundingBoxAscent + m.fontBoundingBoxDescent;
        }
      }
    } catch (e) { /* fall through to DOM probe */ }
    if (!(charW > 0 && charH > 0)) {
      var probe = document.createElement("span");
      probe.textContent = "WWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWW";
      probe.style.cssText =
        "display:inline-block;visibility:hidden;position:absolute;top:0;left:-9999em;" +
        "white-space:pre;font-kerning:none;line-height:normal;" +
        "font-family:" + FONT_FAMILY + ";" +
        "font-size:" + fontSize + "px;";
      document.body.appendChild(probe);
      charW = probe.offsetWidth / 32;
      charH = probe.offsetHeight;
      document.body.removeChild(probe);
    }
    if (!(charW > 0 && charH > 0)) {
      return { w: CHAR_ASPECT * fontSize, h: fontSize };
    }
    var deviceCellW = charW * dpr + Math.round(letterSpacing);
    var deviceCellH = Math.floor(Math.ceil(charH * dpr) * lineHeight);
    return { w: deviceCellW / dpr, h: deviceCellH / dpr };
  }

  // Fit font size to the container so the grid fills it; runs before
  // first output and on every debounced resize.
  function resizeTerminal() {
    if (!term) return;
    var host = document.getElementById("terminal");
    if (!host) return;
    var W = host.clientWidth;
    var H = host.clientHeight;
    if (W < 50 || H < 50) return;

    var dpr = window.devicePixelRatio || 1;
    var coarse = !!(window.matchMedia && window.matchMedia("(pointer: coarse)").matches);

    // DPI floor: glyphs must stay at least ~9 device pixels tall.
    var minFont = Math.max(13, Math.ceil(10.8 / dpr));

    var targetCols = coarse ? 42 : 105;
    if (!coarse && W < 700) targetCols = 90;

    var fontSize = W / (targetCols * CHAR_ASPECT);
    fontSize = Math.min(fontSize, coarse ? 24 : 34);
    fontSize = Math.min(fontSize, H / (1.2 * 10)); // keep >= ~10 visible rows
    fontSize = Math.max(minFont, Math.floor(fontSize));

    if (term.options.fontSize !== fontSize) {
      term.options.fontSize = fontSize;
    }

    var cell = measureCell(fontSize);
    var cols = Math.max(20, Math.floor(W / cell.w));
    var rows = Math.max(8, Math.floor(H / cell.h));
    if (term.cols !== cols || term.rows !== rows) {
      term.resize(cols, rows);
      // Blank the grid: resize reflow desyncs the relative-cursor renderer.
      term.clear();
    }
    term.scrollToBottom();

    // Send one size message; a second refresh would race it and paint stale.
    if (typeof window.v2vSetSize === "function") {
      window.v2vSetSize(term.cols, term.rows);
    }
  }

  function scheduleResize() {
    if (resizeTimer) clearTimeout(resizeTimer);
    resizeTimer = setTimeout(function () {
      resizeTimer = null;
      resizeTerminal();
    }, 150);
  }

  var b64url = function (buf) {
    var bytes = new Uint8Array(buf), bin = "";
    for (var i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
    return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  };
  var b64urlToBytes = function (s) {
    s = s.replace(/-/g, "+").replace(/_/g, "/");
    while (s.length % 4) s += "=";
    var bin = atob(s), u = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) u[i] = bin.charCodeAt(i);
    return u;
  };
  var rnd = function (n) { var u = new Uint8Array(n); crypto.getRandomValues(u); return u; };
  // WebAuthn takes raw challenge bytes; signing the base64url text fails validation.
  var sha256bytes = function (text) {
    return crypto.subtle.digest("SHA-256", new TextEncoder().encode(text));
  };

  // Passkey login bridge.
  // The Go client calls v2vRequestAssertion(nonce, role) during the
  // handshake and waits on v2vAssertionReady(json).
  window.v2vRequestAssertion = function (nonceHex, role) {
    var respond = function (payload) {
      try {
        if (typeof window.v2vAssertionReady === "function") {
          window.v2vAssertionReady(JSON.stringify(payload));
        }
      } catch (e) {
        console.log("Go already exited, ignoring late response", e);
      }
    };
    sha256bytes(nonceHex)
      .then(function (challenge) {
        return navigator.credentials.get({
          publicKey: {
            challenge: challenge,
            rpId: location.hostname,
            userVerification: "preferred",
            timeout: 60000
          }
        });
      })
      .then(function (cred) {
        respond({
          passkey_id: b64url(cred.rawId),
          passkey_auth_data: b64url(cred.response.authenticatorData),
          passkey_client_data: b64url(cred.response.clientDataJSON),
          passkey_sig: b64url(cred.response.signature)
        });
      })
      .catch(function () { respond({}); });
  };

  // Unparsable Retry-After falls back to the submission window.
  var PAIR_RETRY_FALLBACK_SECONDS = 10;

  function retryAfterSeconds(raw) {
    var seconds = parseInt(raw || "", 10);
    if (isNaN(seconds) || seconds < 1) return PAIR_RETRY_FALLBACK_SECONDS;
    return seconds;
  }

  // Map browser spec errors to actionable text; dismissal and timeout
  // share one error, unknown cases keep the raw text in brackets.
  function pairErrorText(e) {
    var raw = (e && e.message) || e;
    switch (e && e.name) {
      case "NotAllowedError":
        return "Bạn đã huỷ, hoặc hết thời gian xác thực. Bấm Thử lại nếu muốn làm lại.";
      case "NotSupportedError":
        return "Trình duyệt hoặc thiết bị này không hỗ trợ passkey.";
      case "SecurityError":
        return "Trình duyệt chặn passkey ở địa chỉ này (sai tên miền, hoặc không phải HTTPS).";
      case "InvalidStateError":
        return "Không có passkey nào dùng được cho máy chủ này.";
      case "AbortError":
        return "Xác thực đã bị dừng giữa chừng.";
      default:
        return "Không xác thực được (" + raw + ").";
    }
  }

  // Desktop pair mode.
  // Sign the desktop ticket and post the assertion; the server already
  // chose the role, so the connect form and settings hide.
  function runPairMode(ticket) {
    panel.style.display = "block";
    wrap.style.display = "none";
    // The form would start a guest session in the waiting tab.
    [form, el("connect-title"), el("connect-hint"), el("config-btn"), el("config-panel")].forEach(function (node) {
      if (node) node.classList.add("hidden");
    });
    if (el("pair-title")) el("pair-title").hidden = false;

    var retryBtn = el("pair-retry");
    var retryTimer = null;

    // Only a request the server saw leaves a cooldown; cancelled dialogs retry at once.
    function armRetry(waitSeconds) {
      if (!retryBtn) return;
      retryBtn.hidden = false;
      if (retryTimer) {
        clearInterval(retryTimer);
        retryTimer = null;
      }
      if (waitSeconds <= 0) {
        retryBtn.disabled = false;
        retryBtn.textContent = "Thử lại passkey";
        return;
      }
      var left = waitSeconds;
      retryBtn.disabled = true;
      retryBtn.textContent = "Thử lại sau " + left + "s";
      retryTimer = setInterval(function () {
        left -= 1;
        if (left > 0) {
          retryBtn.textContent = "Thử lại sau " + left + "s";
          return;
        }
        clearInterval(retryTimer);
        retryTimer = null;
        retryBtn.disabled = false;
        retryBtn.textContent = "Thử lại passkey";
      }, 1000);
    }

    function ask() {
      if (retryBtn) retryBtn.hidden = true;
      setStatus("Đang chờ passkey cho desktop…");
      // Set once the assertion leaves: only then does the server meter start.
      var submitted = false;
      sha256bytes(ticket)
        .then(function (challenge) {
          return navigator.credentials.get({
            publicKey: {
              challenge: challenge,
              rpId: location.hostname,
              userVerification: "required",
              timeout: 120000
            }
          });
        })
        .then(function (cred) {
          submitted = true;
          return gateFetch("/pair/submit", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              ticket: ticket,
              passkey_id: b64url(cred.rawId),
              auth_data: b64url(cred.response.authenticatorData),
              client_data: b64url(cred.response.clientDataJSON),
              sig: b64url(cred.response.signature)
            })
          }, setStatus);
        })
        .then(function (resp) {
          // 410 means the ticket is spent; no retry can revive it.
          if (resp.status === 410) {
            setStatus("❌ Link đã hết hạn hoặc đã dùng. Chạy lại lệnh trên desktop để lấy link mới.", true);
            return;
          }
          if (!resp.ok) {
            return resp.text().then(function (body) {
              setStatus("❌ Chưa xác thực được: " + (body || resp.status), true);
              armRetry(retryAfterSeconds(resp.headers.get("Retry-After")));
            });
          }
          setStatus("✅ Đã xác thực xong. Bạn có thể đóng tab này và quay lại V2V.");
        })
        .catch(function (e) {
          // No server verdict arrived, so no header exists; use the configured window.
          setStatus("❌ " + pairErrorText(e), true);
          armRetry(submitted ? PAIR_RETRY_FALLBACK_SECONDS : 0);
        });
    }

    if (retryBtn) {
      retryBtn.addEventListener("click", ask);
    }
    ask();
  }

  // Desktop-issued enrollment.
  // Create a real passkey from a one-time ticket; only the public half
  // is stored. The panel stays visible for status output.
  function runEnrollMode(code) {
    panel.style.display = "block";
    wrap.style.display = "none";
    console.log("[ENROLL] start ticket=" + code.slice(0, 12) + "…");
    setStatus("Đang tải phiên đăng ký passkey…");
    console.log("[ENROLL] fetching begin for ticket", code.slice(0, 12));
    gateFetch("/webauthn/enroll/begin?ticket=" + encodeURIComponent(code), undefined, setStatus)
      .then(function (r) {
        if (!r.ok) return r.text().then(function (t) { throw new Error(t || r.status); });
        return r.json();
      })
      .then(function (opts) {
        console.log("[ENROLL] begin ok, challenge received, rpId=", opts.publicKey.rp.id);
        var pk = opts.publicKey;
        pk.challenge = b64urlToBytes(pk.challenge);
        pk.user.id = b64urlToBytes(pk.user.id);
        setStatus("Xác nhận trên password manager của bạn…");
        console.log("[ENROLL] calling navigator.credentials.create…");
        return navigator.credentials.create({ publicKey: pk });
      })
      .then(function (cred) {
        console.log("[ENROLL] credential created id=", cred.id.slice(0, 12) + "…");
        setStatus("Đang lưu passkey…");
        var payload = {
          ticket: code,
          id: cred.id,
          client_data_json: b64url(cred.response.clientDataJSON),
          attestation_object: b64url(cred.response.attestationObject)
        };
        console.log("[ENROLL] posting finish for ticket", code.slice(0, 12));
        return gateFetch("/webauthn/enroll/finish", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(payload)
        }, setStatus).then(function (r) {
          console.log("[ENROLL] finish response", r.status);
          if (!r.ok) return r.text().then(function (t) { throw new Error(t || r.status); });
          return r.json();
        });
      })
      .then(function (res) {
        console.log("[ENROLL] finish ok", res);
        setStatus("✅ Passkey đã tạo. Đóng trang này và đăng nhập bằng nút 🔑 Passkey.");
      })
      .catch(function (e) {
        console.error("[ENROLL] failed", e);
        setStatus("Lỗi enroll: " + e.message, true);
      });
  }

  var initialHash = location.hash || "";
  if (initialHash.indexOf("#pair=") === 0) {
    var params = new URLSearchParams(initialHash.slice(1));
    var pairTicket = params.get("pair") || "";
    location.hash = ""; // Drop the ticket so reloads start fresh.
    if (pairTicket) runPairMode(pairTicket);
  } else if (initialHash.indexOf("#enroll=") === 0) {
    var eParams = new URLSearchParams(initialHash.slice(1));
    var enrollCode = eParams.get("enroll") || "";
    location.hash = ""; // Drop the ticket so reloads start fresh.
    if (enrollCode) runEnrollMode(enrollCode);
  }

  function startTerminal() {
    var host = document.getElementById("terminal");
    if (!host) {
      setStatus("Lỗi: #terminal không tồn tại trong DOM.", true);
      return null;
    }
    // Dispose previous terminal to avoid duplicate xterm-owner-* stacking.
    if (term) {
      try { term.dispose(); } catch (e) {}
      term = null;
    }
    host.innerHTML = "";
    // Remove old listeners to avoid duplicate scheduleResize calls.
    window.removeEventListener("resize", scheduleResize);
    if (window.visualViewport) {
      try { window.visualViewport.removeEventListener("resize", scheduleResize); } catch (e) {}
    }
    window.removeEventListener("orientationchange", scheduleResize);

    term = new Terminal({
      cursorBlink: true,
      fontSize: 14,
      fontFamily: FONT_FAMILY,
      convertEol: true,
      // The app scrolls its own chat viewport; terminal scrollback would
      // add a second scrollbar.
      scrollback: 0,
      theme: { background: "#101014" },
    });

    term.open(host);

    // Route link clicks by hand: trip links copy then confirm, external
    // links ask first with the URL shown. The expand trailer is a button,
    // not a link, so it needs no handling here.
    try {
      term.options.linkHandler = {
        allowNonHttpProtocols: true,
        activate: function (e, uri) {
          if (uri && (uri.indexOf("v2v://trip") === 0 || uri.indexOf("/api/trip/verify") !== -1)) {
            if (e && e.preventDefault) e.preventDefault();
            if (uri.indexOf("/api/trip/verify") !== -1) {
              gateFetch(uri, undefined, setStatus).then(function (r) { return r.json(); }).then(function (j) {
                var info = j.valid ? "✅ Trip valid " + (j.badge || "") + " seq=" + j.seq : "❌ Trip invalid: " + (j.error || "");
                setStatus(info, !j.valid);
              }).catch(function () { setStatus("Trip link: " + uri, false); });
              if (navigator.clipboard) navigator.clipboard.writeText(uri).catch(function () {});
              // URL is already on the clipboard; offer the readable page.
              if (window.confirm("Đã copy link verify. Mở trang kiểm tra trong tab mới?")) {
                window.open(uri, "_blank", "noopener");
              }
            } else {
              var q = uri.split("?")[1] || "";
              var p = new URLSearchParams(q);
              var info2 = "Trip pub=" + (p.get("pub") || "").slice(0, 12) + "… seq=" + (p.get("seq") || "?");
              setStatus(info2, false);
              if (navigator.clipboard) navigator.clipboard.writeText(uri).catch(function () {});
            }
            return;
          }
          // External content always asks first with the URL shown; only
          // http(s) opens, so crafted schemes never reach window.open.
          if (uri && /^https?:\/\//i.test(uri)) {
            if (e && e.preventDefault) e.preventDefault();
            if (window.confirm("Mở liên kết ngoài?\n\n" + uri)) {
              window.open(uri, "_blank", "noopener");
            }
          }
        }
      };
    } catch (err) {}

    window.addEventListener("resize", scheduleResize);
    if (window.visualViewport) {
      window.visualViewport.addEventListener("resize", scheduleResize);
    }
    window.addEventListener("orientationchange", scheduleResize);

    // Wait for fonts so the first measure is accurate.
    if (document.fonts && document.fonts.ready) {
      document.fonts.ready.then(function () { resizeTerminal(); });
    }
    resizeTerminal();

    term.onData(function (data) {
      if (data) window.v2vSendKeys(data);
    });

    window.v2vOutput = function (s) {
      if (term) term.write(s);
    };

    // After /quit reload to the connect panel, not a dead terminal.
    window.v2vExit = function () {
      location.reload();
    };

    return host;
  }

  function bootWasm() {
    var host = startTerminal();
    if (!host) return;

    var go = new Go();
    var wasmPath = "app.wasm?v=" + encodeURIComponent(VERSION);
    var importObject = go.importObject;

    function run(instance) {
      go.run(instance);
    }

    if ("instantiateStreaming" in WebAssembly) {
      WebAssembly.instantiateStreaming(fetch(wasmPath), importObject).then(function (result) {
        run(result.instance);
      }).catch(function () {
        // Fallback: some proxies strip the application/wasm content type.
        return fetch(wasmPath).then(function (resp) { return resp.arrayBuffer(); })
          .then(function (buf) { return WebAssembly.instantiate(buf, importObject); })
          .then(function (result) { run(result.instance); })
          .catch(function (err2) {
            setStatus("Không thể tải app.wasm: " + err2, true);
            connectBtn.disabled = false;
          });
      });
    } else {
      fetch(wasmPath).then(function (resp) { return resp.arrayBuffer(); })
        .then(function (buf) { return WebAssembly.instantiate(buf, importObject); })
        .then(function (result) { run(result.instance); })
        .catch(function (err) {
          setStatus("Không thể tải app.wasm: " + err, true);
          connectBtn.disabled = false;
        });
    }
  }

  form.addEventListener("submit", function (e) {
    e.preventDefault();

    if (typeof Go === "undefined") {
      setStatus(
        "Lỗi: wasm_exec.js chưa được tải (thiếu file build? Chạy ./build_web.sh trước khi deploy).",
        true
      );
      return;
    }

    // Cached pages can mix markup and script versions; check before
    // disabling the button or a mismatch leaves it stuck.
    if (!form || !connectBtn || !serverInput || !userInput) {
      setStatus("Lỗi: trang không khớp phiên bản — tải lại (Ctrl+Shift+R).", true);
      return;
    }

    connectBtn.disabled = true;
    setStatus("Đang kết nối...");

    var origin = location.origin + "/";
    var server = serverInput.value.trim();
    if (!server) server = origin;

    window.v2vConfig = {
      serverUrl: server,
      username: userInput.value.trim(),
      tripcode: tripInput.value.trim(),
      showJoin: opts.showJoin,
      showMeta: opts.meta,
      autoVerify: opts.autoVerify,
      powMinTier: opts.powMinTier,
      notify: opts.notify,
    };
    // Scrub the passphrase from the DOM once copied.
    tripInput.value = "";

    if (usePasskey) {
      var pkRole = passkeyRoleInput.value.trim();
      if (!pkRole) {
        // Fail fast without a role instead of falling back to a guest session.
        setStatus("Nhập role cho đăng nhập bằng Passkey.", true);
        connectBtn.disabled = false;
        return;
      }
      window.v2vConfig.passkey = true;
      window.v2vConfig.passkeyRole = pkRole;
    }

    panel.style.display = "none";
    wrap.style.display = "block";
    bootWasm();
  });

  serverInput.value = location.origin + "/";
  userInput.focus();
})();