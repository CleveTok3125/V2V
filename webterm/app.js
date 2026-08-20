/* Phase 1: minimal xterm page that echoes everything typed back.
 * Verifies the terminal renders and accepts input in the browser before any
 * Go/wasm chat logic is layered on. */
(function () {
  "use strict";

  var term = new Terminal({
    cursorBlink: true,
    fontSize: 14,
    fontFamily: '"JetBrains Mono", "DejaVu Sans Mono", monospace',
    scrollback: 10000,
    theme: { background: "#101014" },
  });

  term.open(document.getElementById("terminal"));

  term.write("V2V web terminal (self-test). Gõ thử vài ký tự...\r\n\r\n");

  term.onData(function (data) {
    term.write(data);
  });
})();