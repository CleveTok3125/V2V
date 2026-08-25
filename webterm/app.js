/* V2V web terminal.
 * Thin glue between the browser terminal (xterm.js) and the Go WASM client.
 * All line editing (echo, backspace, escape sequences, prompts) is handled
 * by the Go client; JS only forwards keystrokes and renders output.
 *  - window.v2vSendKeys(s) : browser -> Go, raw keystroke data
 *  - window.v2vOutput(s)   : Go -> browser, append string to the terminal
 *  - window.v2vConfig      : connection config the Go client reads at startup
 */
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
  var showJoinToggle = document.getElementById("showjoin");
  var passkeyBtn = document.getElementById("passkey-btn");
  var passkeyRoleInput = document.getElementById("passkey-role");
  var usePasskey = false;

  if (passkeyBtn) {
    passkeyBtn.addEventListener("click", function () {
      var hidden = passkeyRoleInput.style.display === "none";
      if (hidden) {
        passkeyRoleInput.style.display = "block";
        passkeyRoleInput.focus();
        passkeyBtn.textContent = "🔑 Passkey (đã chọn)";
        usePasskey = true;
      } else {
        usePasskey = !usePasskey;
        passkeyBtn.textContent = usePasskey ? "🔑 Passkey (đã chọn)" : "🔑 Passkey";
      }
    });
  }

  function setStatus(msg, isError) {
    statusLine.textContent = msg;
    statusLine.className = isError ? "error" : "";
  }

  var FONT_FAMILY = '"JetBrains Mono", Menlo, Consolas, "DejaVu Sans Mono", monospace';

  // Monospace advance width ~= 0.6em, used for the initial font-size guess.
  var CHAR_ASPECT = 0.6;
  var resizeTimer = null;

  // measureCell returns the rendered size of one character cell for the
  // terminal font at the given px size, measured with a detached probe span.
  function measureCell(fontSize) {
    var probe = document.createElement("span");
    probe.style.cssText =
      "position:absolute;top:-9999px;left:0;visibility:hidden;white-space:pre;" +
      "font-family:" + FONT_FAMILY + ";" +
      "font-size:" + fontSize + "px;line-height:normal;";
    probe.textContent = "MMMMMMMMMM";
    document.body.appendChild(probe);
    var rect = probe.getBoundingClientRect();
    var cell = { w: rect.width / 10, h: rect.height };
    document.body.removeChild(probe);
    return cell;
  }

  // resizeTerminal implements dynamic scale + fit: the font size is derived
  // from the container so the grid fills the available space, with signals
  // from devicePixelRatio (crispness floor, browser-zoom aware) and pointer
  // coarseness (mobile gets fewer, larger columns). It runs before any wasm
  // output so long messages wrap correctly from the first line, and again on
  // every resize/orientation change (debounced); xterm reflows the buffer,
  // and v2vRefresh lets the Go side redraw prompt + draft afterwards.
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
    }
    term.scrollToBottom();

    // Tell the Go editor the new grid width (it wraps multi-row drafts on
    // it), then let it repaint prompt + draft at the new geometry.
    if (typeof window.v2vSetSize === "function") {
      window.v2vSetSize(term.cols, term.rows);
    }
    if (typeof window.v2vRefresh === "function") {
      window.v2vRefresh();
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
  var rnd = function (n) { var u = new Uint8Array(n); crypto.getRandomValues(u); return u; };
  var sha256b64url = function (text) {
    return crypto.subtle.digest("SHA-256", new TextEncoder().encode(text)).then(b64url);
  };

  // --- Passkey login bridge -----------------------------------------------
  // The Go client calls v2vRequestAssertion(nonce, role) during the WebSocket
  // handshake and waits on v2vAssertionReady(json).
  window.v2vRequestAssertion = function (nonceHex, role) {
    var respond = function (payload) {
      if (typeof window.v2vAssertionReady === "function") {
        window.v2vAssertionReady(JSON.stringify(payload));
      }
    };
    crypto.subtle.digest("SHA-256", new TextEncoder().encode(nonceHex))
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

  // --- Desktop pair mode (#pair=<nonce>) ----------------------------------
  // Desktop prints this URL; the assertion is posted back to the server so
  // the desktop's own handshake can consume the same nonce.
  function runPairMode(nonce, role) {
    panel.style.display = "none";
    wrap.style.display = "block";
    setStatus("Đang chờ passkey cho desktop…");
    sha256b64url(nonce).then(function (challenge) {
      return navigator.credentials.get({
        publicKey: {
          challenge: challenge,
          rpId: location.hostname,
          userVerification: "preferred",
          timeout: 120000
        }
      });
    }).then(function (cred) {
      return fetch("/pair/submit", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          nonce: nonce,
          role: role || "",
          passkey_id: b64url(cred.rawId),
          auth_data: b64url(cred.response.authenticatorData),
          client_data: b64url(cred.response.clientDataJSON),
          sig: b64url(cred.response.signature)
        })
      });
    }).then(function (resp) {
      if (!resp.ok) throw new Error("server từ chối (" + resp.status + ")");
      setStatus("✅ Đã xác thực — quay lại cửa sổ V2V trên máy của bạn.");
    }).catch(function (e) {
      setStatus("Lỗi pair: " + e.message, true);
    });
  }

  var initialHash = location.hash || "";
  if (initialHash.indexOf("#pair=") === 0) {
    var params = new URLSearchParams(initialHash.slice(1));
    var pairNonce = params.get("pair") || "";
    var pairRole = params.get("role") || "";
    location.hash = ""; // clean up so reloads start fresh
    if (pairNonce) runPairMode(pairNonce, pairRole);
  }

  function startTerminal() {
    var host = document.getElementById("terminal");
    if (!host) {
      setStatus("Lỗi: #terminal không tồn tại trong DOM.", true);
      return null;
    }

    term = new Terminal({
      cursorBlink: true,
      fontSize: 14,
      fontFamily: FONT_FAMILY,
      convertEol: true,
      scrollback: 10000,
      theme: { background: "#101014" },
    });

    term.open(host);

    window.addEventListener("resize", scheduleResize);
    if (window.visualViewport) {
      window.visualViewport.addEventListener("resize", scheduleResize);
    }
    window.addEventListener("orientationchange", scheduleResize);

    // Size the grid to the real viewport before any output exists.
    resizeTerminal();

    term.onData(function (data) {
      if (data) window.v2vSendKeys(data);
    });

    window.v2vOutput = function (s) {
      if (term) term.write(s);
    };

    // The Go client calls this after /quit so the page reloads back to the
    // connect panel instead of leaving a dead terminal on screen.
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

    connectBtn.disabled = true;
    setStatus("Đang kết nối...");

    var origin = location.origin + "/";
    var server = serverInput.value.trim();
    if (!server) server = origin;

    window.v2vConfig = {
      serverUrl: server,
      username: userInput.value.trim(),
      tripcode: tripInput.value.trim(),
      showJoin: showJoinToggle.checked,
    };

    panel.style.display = "none";
    wrap.style.display = "block";
    bootWasm();
  });

  serverInput.value = location.origin + "/";
  userInput.focus();
})();