// @ts-nocheck -- migrated legacy UI; typing is incremental.
/* V2V web terminal.
 * Thin glue between the browser terminal (xterm.js) and the Go WASM client.
 * All line editing (echo, backspace, escape sequences, prompts) is handled
 * by the Go client; JS only forwards keystrokes and renders output.
 *  - window.v2vSendKeys(s) : browser -> Go, raw keystroke data
 *  - window.v2vOutput(s)   : Go -> browser, append string to the terminal
 *  - window.v2vConfig      : connection config the Go client reads at startup
 */
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
                if (passkeyRoleLabel)
                    passkeyRoleLabel.style.display = "block";
                passkeyRoleInput.focus();
                passkeyBtn.textContent = "🔑 Passkey (đã chọn)";
                usePasskey = true;
            }
            else {
                passkeyRoleInput.style.display = "none";
                if (passkeyRoleLabel)
                    passkeyRoleLabel.style.display = "none";
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
                    // Move cursor to end after parsing
                    try {
                        passkeyRoleInput.setSelectionRange(rolePart.length, rolePart.length);
                    }
                    catch (e) { }
                }
            }
        });
    }
    function setStatus(msg, isError) {
        statusLine.textContent = msg;
        statusLine.className = isError ? "error" : "";
    }
    // ---- Settings panel ----------------------------------------------------
    // Mirrors the toggle subset of the desktop client config (ui.meta.show,
    // join/leave notices, defaults.autoVerify, ui.notify.*). Applied when
    // connecting, so the panel is the pre-connect surface for these; the live
    // session still owns them afterwards through /meta, /showjoin,
    // /autoverify, /notify.
    var OPT_DEFAULTS = {
        meta: true,
        showJoin: false,
        autoVerify: true,
        notify: { pow: true, powMinTier: 1, history: true, join: true, date: true, system: true },
    };
    var OPT_KEY = "v2v.config";
    var opts = cloneOpts(OPT_DEFAULTS);
    function cloneOpts(src) {
        return {
            meta: src.meta,
            showJoin: src.showJoin,
            autoVerify: src.autoVerify,
            notify: {
                pow: src.notify.pow,
                powMinTier: src.notify.powMinTier,
                history: src.notify.history,
                join: src.notify.join,
                date: src.notify.date,
                system: src.notify.system,
            },
        };
    }
    // sessionStorage is the default: the choices survive a reload (which is
    // what /quit does) but die with the tab. "Lưu lâu dài" additionally
    // mirrors into localStorage so they outlive the browser session.
    function loadOpts() {
        var raw = null;
        try {
            raw = window.localStorage.getItem(OPT_KEY) || window.sessionStorage.getItem(OPT_KEY);
        }
        catch (e) { /* storage may be blocked; defaults stand */ }
        if (!raw)
            return;
        try {
            var got = JSON.parse(raw);
            if (got && typeof got === "object") {
                ["meta", "showJoin", "autoVerify"].forEach(function (k) {
                    if (typeof got[k] === "boolean")
                        opts[k] = got[k];
                });
                if (got.notify && typeof got.notify === "object") {
                    ["pow", "history", "join", "date", "system"].forEach(function (k) {
                        if (typeof got.notify[k] === "boolean")
                            opts.notify[k] = got.notify[k];
                    });
                    if (typeof got.notify.powMinTier === "number" && got.notify.powMinTier >= 1) {
                        opts.notify.powMinTier = Math.floor(got.notify.powMinTier);
                    }
                }
            }
        }
        catch (e) { /* corrupt payload: keep defaults */ }
    }
    function persistEnabled() {
        try {
            return !!window.localStorage.getItem(OPT_KEY);
        }
        catch (e) {
            return false;
        }
    }
    function saveOpts() {
        var raw = JSON.stringify(opts);
        try {
            window.sessionStorage.setItem(OPT_KEY, raw);
        }
        catch (e) { /* ignore */ }
        if (persistEnabled()) {
            try {
                window.localStorage.setItem(OPT_KEY, raw);
            }
            catch (e) { /* ignore */ }
        }
    }
    function clearStoredOpts() {
        try {
            window.sessionStorage.removeItem(OPT_KEY);
        }
        catch (e) { /* ignore */ }
        try {
            window.localStorage.removeItem(OPT_KEY);
        }
        catch (e) { /* ignore */ }
    }
    function el(id) { return document.getElementById(id); }
    function syncJoinDep() {
        // notify.join only has meaning while join/leave render at all.
        var row = el("opt-notifyjoin-row");
        if (!row)
            return;
        var input = el("opt-notifyjoin");
        input.disabled = !opts.showJoin;
    }
    function applyOptsToUI() {
        el("opt-meta").checked = opts.meta;
        el("opt-showjoin").checked = opts.showJoin;
        el("opt-autoverify").checked = opts.autoVerify;
        el("opt-notifypow").checked = opts.notify.pow;
        el("opt-powmintier").value = String(opts.notify.powMinTier);
        el("opt-notifyhistory").checked = opts.notify.history;
        el("opt-notifyjoin").checked = opts.notify.join;
        el("opt-notifydate").checked = opts.notify.date;
        el("opt-notify-system").checked = opts.notify.system;
        el("opt-persist").checked = persistEnabled();
        syncJoinDep();
    }
    // [options path, input id] — listed explicitly so the wiring cannot drift
    // from the markup the way a computed id would.
    var OPT_SWITCHES = [
        ["meta", "opt-meta"],
        ["showJoin", "opt-showjoin"],
        ["autoVerify", "opt-autoverify"],
        ["notify.pow", "opt-notifypow"],
        ["notify.history", "opt-notifyhistory"],
        ["notify.join", "opt-notifyjoin"],
        ["notify.date", "opt-notifydate"],
        ["notify.system", "opt-notify-system"],
    ];
    function readOpt(path) {
        var parts = path.split(".");
        var v = opts;
        for (var i = 0; i < parts.length; i++)
            v = v[parts[i]];
        return v;
    }
    function writeOpt(path, value) {
        var parts = path.split(".");
        var last = parts.pop();
        var v = opts;
        for (var i = 0; i < parts.length; i++)
            v = v[parts[i]];
        v[last] = value;
    }
    function initConfigPanel() {
        var btn = el("config-btn");
        var panel = el("config-panel");
        if (!btn || !panel)
            return;
        loadOpts();
        applyOptsToUI();
        btn.addEventListener("click", function () {
            var open = panel.classList.contains("hidden");
            panel.classList.toggle("hidden");
            btn.textContent = open ? "⚙️ Ẩn tuỳ chọn" : "⚙️ Tuỳ chọn";
        });
        OPT_SWITCHES.forEach(function (pair) {
            var input = el(pair[1]);
            if (!input)
                return;
            input.addEventListener("change", function () {
                writeOpt(pair[0], this.checked);
                if (pair[0] === "showJoin")
                    syncJoinDep();
                saveOpts();
            });
        });
        var tier = el("opt-powmintier");
        if (tier) {
            tier.addEventListener("change", function () {
                writeOpt("notify.powMinTier", Math.max(1, Math.floor(Number(this.value) || 1)));
                this.value = String(readOpt("notify.powMinTier"));
                saveOpts();
            });
        }
        var persist = el("opt-persist");
        if (persist) {
            persist.addEventListener("change", function () {
                if (this.checked) {
                    try {
                        window.localStorage.setItem(OPT_KEY, JSON.stringify(opts));
                    }
                    catch (e) { /* ignore */ }
                }
                else {
                    try {
                        window.localStorage.removeItem(OPT_KEY);
                    }
                    catch (e) { /* ignore */ }
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
    // Expose status setter for WASM to show auth errors without a full page reload.
    window.v2vSetStatus = function (msg, isError) {
        setStatus(msg, !!isError);
        if (isError) {
            // Show the login panel so the error is visible (it was hidden before
            // bootWasm). Keep the terminal hidden.
            panel.style.display = "block";
            wrap.style.display = "none";
        }
        // Re-enable the connect button so the user can retry.
        if (connectBtn)
            connectBtn.disabled = false;
    };
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
        if (!term)
            return;
        var host = document.getElementById("terminal");
        if (!host)
            return;
        var W = host.clientWidth;
        var H = host.clientHeight;
        if (W < 50 || H < 50)
            return;
        var dpr = window.devicePixelRatio || 1;
        var coarse = !!(window.matchMedia && window.matchMedia("(pointer: coarse)").matches);
        // DPI floor: glyphs must stay at least ~9 device pixels tall.
        var minFont = Math.max(13, Math.ceil(10.8 / dpr));
        var targetCols = coarse ? 42 : 105;
        if (!coarse && W < 700)
            targetCols = 90;
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
        if (resizeTimer)
            clearTimeout(resizeTimer);
        resizeTimer = setTimeout(function () {
            resizeTimer = null;
            resizeTerminal();
        }, 150);
    }
    var b64url = function (buf) {
        var bytes = new Uint8Array(buf), bin = "";
        for (var i = 0; i < bytes.length; i++)
            bin += String.fromCharCode(bytes[i]);
        return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
    };
    var b64urlToBytes = function (s) {
        s = s.replace(/-/g, "+").replace(/_/g, "/");
        while (s.length % 4)
            s += "=";
        var bin = atob(s), u = new Uint8Array(bin.length);
        for (var i = 0; i < bin.length; i++)
            u[i] = bin.charCodeAt(i);
        return u;
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
            try {
                if (typeof window.v2vAssertionReady === "function") {
                    window.v2vAssertionReady(JSON.stringify(payload));
                }
            }
            catch (e) {
                console.log("Go already exited, ignoring late response", e);
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
            if (!resp.ok)
                throw new Error("server từ chối (" + resp.status + ")");
            setStatus("✅ Đã xác thực — quay lại cửa sổ V2V trên máy của bạn.");
        }).catch(function (e) {
            setStatus("Lỗi pair: " + e.message, true);
        });
    }
    // --- Desktop-issued enrollment (#enroll=<code>) -------------------------
    // Admin issues a one-time ticket (server -enroll); the member opens the
    // URL, the browser popup creates a REAL passkey in their password manager,
    // and only the public half is stored server-side.
    // Keep the connect panel visible so setStatus remains visible (it lives
    // inside the panel); the terminal wrap stays hidden during enrollment.
    function runEnrollMode(code) {
        // Ensure panel is visible and wrap hidden for clear status feedback.
        panel.style.display = "block";
        wrap.style.display = "none";
        console.log("[ENROLL] start ticket=" + code.slice(0, 12) + "…");
        setStatus("Đang tải phiên đăng ký passkey…");
        console.log("[ENROLL] fetching begin for ticket", code.slice(0, 12));
        gateFetch("/webauthn/enroll/begin?ticket=" + encodeURIComponent(code), undefined, setStatus)
            .then(function (r) {
            if (!r.ok)
                return r.text().then(function (t) { throw new Error(t || r.status); });
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
                if (!r.ok)
                    return r.text().then(function (t) { throw new Error(t || r.status); });
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
        var pairNonce = params.get("pair") || "";
        var pairRole = params.get("role") || "";
        location.hash = ""; // clean up so reloads start fresh
        if (pairNonce)
            runPairMode(pairNonce, pairRole);
    }
    else if (initialHash.indexOf("#enroll=") === 0) {
        var eParams = new URLSearchParams(initialHash.slice(1));
        var enrollCode = eParams.get("enroll") || "";
        location.hash = "";
        if (enrollCode)
            runEnrollMode(enrollCode);
    }
    function startTerminal() {
        var host = document.getElementById("terminal");
        if (!host) {
            setStatus("Lỗi: #terminal không tồn tại trong DOM.", true);
            return null;
        }
        // Dispose previous terminal to avoid duplicate xterm-owner-* stacking.
        if (term) {
            try {
                term.dispose();
            }
            catch (e) { }
            term = null;
        }
        host.innerHTML = "";
        // Remove old listeners to avoid duplicate scheduleResize calls.
        window.removeEventListener("resize", scheduleResize);
        if (window.visualViewport) {
            try {
                window.visualViewport.removeEventListener("resize", scheduleResize);
            }
            catch (e) { }
        }
        window.removeEventListener("orientationchange", scheduleResize);
        term = new Terminal({
            cursorBlink: true,
            fontSize: 14,
            fontFamily: FONT_FAMILY,
            convertEol: true,
            scrollback: 10000,
            theme: { background: "#101014" },
        });
        term.open(host);
        // Link clicks are routed by hand because every destination is either
        // an in-app command (v2v://expand), a trip verification (copied, then
        // confirmed before a tab opens) or external content that always asks
        // first and shows its URL.
        try {
            term.options.linkHandler = {
                allowNonHttpProtocols: true,
                activate: function (e, uri) {
                    if (uri && uri.indexOf("v2v://expand/") === 0) {
                        if (e && e.preventDefault)
                            e.preventDefault();
                        // Inject a clean command line: Ctrl+U kills any draft the
                        // user was typing, then the expand command submits itself.
                        var h = uri.slice("v2v://expand/".length).replace(/[^0-9]/g, "");
                        if (h && window.v2vSendKeys)
                            window.v2vSendKeys("\x15/expand #" + h + "\n");
                        return;
                    }
                    if (uri && (uri.indexOf("v2v://trip") === 0 || uri.indexOf("/api/trip/verify") !== -1)) {
                        if (e && e.preventDefault)
                            e.preventDefault();
                        if (uri.indexOf("/api/trip/verify") !== -1) {
                            gateFetch(uri, undefined, setStatus).then(function (r) { return r.json(); }).then(function (j) {
                                var info = j.valid ? "✅ Trip valid " + (j.badge || "") + " seq=" + j.seq : "❌ Trip invalid: " + (j.error || "");
                                setStatus(info, !j.valid);
                            }).catch(function () { setStatus("Trip link: " + uri, false); });
                            if (navigator.clipboard)
                                navigator.clipboard.writeText(uri).catch(function () { });
                            // The URL is already on the clipboard; offer to open the
                            // human-readable verify page in a new tab. If a popup
                            // blocker refuses window.open, the copied link remains the
                            // fallback and nothing else happens.
                            if (window.confirm("Đã copy link verify. Mở trang kiểm tra trong tab mới?")) {
                                window.open(uri, "_blank", "noopener");
                            }
                        }
                        else {
                            var q = uri.split("?")[1] || "";
                            var p = new URLSearchParams(q);
                            var info2 = "Trip pub=" + (p.get("pub") || "").slice(0, 12) + "… seq=" + (p.get("seq") || "?");
                            setStatus(info2, false);
                            if (navigator.clipboard)
                                navigator.clipboard.writeText(uri).catch(function () { });
                        }
                        return;
                    }
                    // Any remaining link is plain external content. Ask first and
                    // show the destination so the user can judge where it leads;
                    // only http(s) is ever opened, so a crafted scheme can never
                    // reach window.open. The client already strips non-http(s) OSC8
                    // targets, this is the second gate.
                    if (uri && /^https?:\/\//i.test(uri)) {
                        if (e && e.preventDefault)
                            e.preventDefault();
                        if (window.confirm("Mở liên kết ngoài?\n\n" + uri)) {
                            window.open(uri, "_blank", "noopener");
                        }
                    }
                }
            };
        }
        catch (err) { }
        window.addEventListener("resize", scheduleResize);
        if (window.visualViewport) {
            window.visualViewport.addEventListener("resize", scheduleResize);
        }
        window.addEventListener("orientationchange", scheduleResize);
        // Size the grid to the real viewport before any output exists.
        // Wait for fonts to be ready so measureCell is accurate.
        if (document.fonts && document.fonts.ready) {
            document.fonts.ready.then(function () { resizeTerminal(); });
        }
        resizeTerminal();
        term.onData(function (data) {
            if (data)
                window.v2vSendKeys(data);
        });
        window.v2vOutput = function (s) {
            if (term)
                term.write(s);
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
        if (!host)
            return;
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
        }
        else {
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
            setStatus("Lỗi: wasm_exec.js chưa được tải (thiếu file build? Chạy ./build_web.sh trước khi deploy).", true);
            return;
        }
        // index.html and app.js revalidate independently, so a cached page can
        // briefly pair one version's markup with another's script. Check before
        // disabling the button, otherwise a mismatch throws here and leaves the
        // form stuck on "Đang kết nối..." with a dead button.
        if (!form || !connectBtn || !serverInput || !userInput) {
            setStatus("Lỗi: trang không khớp phiên bản — tải lại (Ctrl+Shift+R).", true);
            return;
        }
        connectBtn.disabled = true;
        setStatus("Đang kết nối...");
        var origin = location.origin + "/";
        var server = serverInput.value.trim();
        if (!server)
            server = origin;
        window.v2vConfig = {
            serverUrl: server,
            username: userInput.value.trim(),
            tripcode: tripInput.value.trim(),
            showJoin: opts.showJoin,
            showMeta: opts.meta,
            autoVerify: opts.autoVerify,
            notify: {
                pow: opts.notify.pow,
                powMinTier: opts.notify.powMinTier,
                history: opts.notify.history,
                join: opts.notify.join,
                date: opts.notify.date,
                system: opts.notify.system,
            },
        };
        // Clear passphrase from DOM immediately after copying to WASM
        tripInput.value = "";
        if (usePasskey) {
            var pkRole = passkeyRoleInput.value.trim();
            if (!pkRole) {
                // A passkey login without a role cannot be honored; fail fast here
                // instead of letting the client fall back to a guest session.
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
