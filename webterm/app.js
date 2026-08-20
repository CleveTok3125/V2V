/* Phase 1: minimal xterm page that echoes everything typed back.
 * Verifies the terminal renders and accepts input in the browser before any
 * Go/wasm chat logic is layered on. */
(function () {
  "use strict";

  function fail(msg) {
    var host = document.getElementById("terminal");
    var out = document.createElement("pre");
    out.textContent = msg;
    if (host) host.appendChild(out);
    else document.body.textContent = msg;
  }

  function boot() {
    var host = document.getElementById("terminal");
    if (!host) {
      fail("Lỗi: #terminal không tồn tại trong DOM.");
      return;
    }

    var term = new Terminal({
      cursorBlink: true,
      fontSize: 14,
      fontFamily: '"JetBrains Mono", "DejaVu Sans Mono", monospace',
      scrollback: 10000,
      theme: { background: "#101014" },
    });

    term.open(host);

    term.write("V2V web terminal (self-test). Gõ thử vài ký tự...\r\n\r\n");

    term.onData(function (data) {
      term.write(data);
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", boot);
  } else {
    boot();
  }
})();