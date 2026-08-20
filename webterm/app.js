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

  var term = null;
  var pendingHigh = null;

  // xterm delivers keystrokes one UTF-16 code unit at a time, so astral
  // characters (emoji, ...) can arrive as a split surrogate pair. Recombine
  // the pair here, before the Go WASM bridge encodes the string as UTF-8
  // (TextEncoder would otherwise replace each lone surrogate with U+FFFD).
  function cleanInput(data) {
    var out = "";
    var i = 0;
    if (pendingHigh !== null) {
      var first = data.charCodeAt(0);
      if (first >= 0xdc00 && first <= 0xdfff) {
        out += String.fromCharCode(pendingHigh, first);
        i = 1;
      } else {
        out += "\uFFFD";
      }
      pendingHigh = null;
    }
    for (; i < data.length; i++) {
      var c = data.charCodeAt(i);
      if (c >= 0xd800 && c <= 0xdbff) {
        if (i + 1 < data.length) {
          var c1 = data.charCodeAt(i + 1);
          if (c1 >= 0xdc00 && c1 <= 0xdfff) {
            out += data[i] + data[i + 1];
            i++;
          } else {
            out += "\uFFFD";
          }
        } else {
          pendingHigh = c;
        }
      } else if (c >= 0xdc00 && c <= 0xdfff) {
        out += "\uFFFD";
      } else {
        out += data[i];
      }
    }
    return out;
  }

  var panel = document.getElementById("connect-panel");
  var wrap = document.getElementById("terminal-wrap");
  var statusLine = document.getElementById("status-line");
  var form = document.getElementById("connect-form");
  var serverInput = document.getElementById("server-url");
  var userInput = document.getElementById("username");
  var tripInput = document.getElementById("tripcode");
  var connectBtn = document.getElementById("connect-btn");
  var showJoinToggle = document.getElementById("showjoin");

  function setStatus(msg, isError) {
    statusLine.textContent = msg;
    statusLine.className = isError ? "error" : "";
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
      fontFamily: '"JetBrains Mono", Menlo, Consolas, "DejaVu Sans Mono", monospace',
      convertEol: true,
      scrollback: 10000,
      theme: { background: "#101014" },
    });

    term.open(host);

    term.onData(function (data) {
      data = cleanInput(data);
      if (data) window.v2vSendKeys(data);
    });

    window.v2vOutput = function (s) {
      if (term) term.write(s);
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