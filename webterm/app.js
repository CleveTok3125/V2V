/* V2V web terminal.
 * Bridges the browser terminal (xterm.js) with the Go WASM client:
 *  - window.v2vOutput(s)   : Go -> browser, append string to the terminal
 *  - window.v2vSendLine(s) : browser -> Go, complete input line
 *  - window.v2vConfig      : connection config the Go client reads at startup
 */
(function () {
  "use strict";

  var VERSION = (typeof window.V2V_VERSION !== "undefined")
    ? window.V2V_VERSION
    : "dev";

  var term = null;
  var lineBuffer = "";
  var pendingPrompt = "";
  var promptActive = false;
  var escBuffer = "";

  var panel = document.getElementById("connect-panel");
  var wrap = document.getElementById("terminal-wrap");
  var statusLine = document.getElementById("status-line");
  var form = document.getElementById("connect-form");
  var serverInput = document.getElementById("server-url");
  var userInput = document.getElementById("username");
  var tripInput = document.getElementById("tripcode");
  var connectBtn = document.getElementById("connect-btn");

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
      for (var i = 0; i < data.length; i++) {
        var ch = data[i];

        if (escBuffer.length > 0) {
          // Consuming an escape sequence (arrow keys, Home/End, ...).
          escBuffer += ch;
          var finished = false;
          if (escBuffer[1] === "[") {
            // CSI: ESC [ ... final byte in 0x40..0x7e
            var code = ch.charCodeAt(0);
            finished = (code >= 0x40 && code <= 0x7e);
          } else {
            // SS3 (ESC O x) or a single-char escape.
            finished = true;
          }
          if (finished) escBuffer = "";
          continue;
        }
        if (ch === "\x1b") {
          escBuffer = ch;
          continue;
        }
        if (ch === "\r") {
          term.write("\r\n");
          window.v2vSendLine(lineBuffer);
          lineBuffer = "";
          promptActive = false;
        } else if (ch === "\x7f") {
          if (lineBuffer.length > 0) {
            lineBuffer = lineBuffer.slice(0, -1);
            term.write("\b \b");
          }
        } else if (ch === "\x03") {
          // Ctrl+C: clear the current line.
          term.write("\r\n");
          lineBuffer = "";
        } else if (ch >= " ") {
          lineBuffer += ch;
          term.write(ch);
        }
      }
    });

    window.v2vOutput = function (s) {
      if (!term) return;
      if (s === "| > " || s === "| ... ") {
        // The Go client just printed an input prompt.
        pendingPrompt = s;
        promptActive = true;
        term.write(s);
        return;
      }
      if (lineBuffer.length > 0) {
        // Incoming text while the user is typing: keep the draft line intact.
        term.write("\r\x1b[K");
        term.write(s);
        if (promptActive) term.write(pendingPrompt);
        term.write(lineBuffer);
      } else {
        term.write(s);
      }
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
    };

    panel.style.display = "none";
    wrap.style.display = "block";
    bootWasm();
  });

  serverInput.value = location.origin + "/";
  userInput.focus();
})();