// @ts-nocheck -- migrated legacy UI; typing is incremental.
import { gateFetch } from "./pow_bridge.js";
(function () {
    "use strict";
    var pill = document.getElementById("pill");
    var fieldsEl = document.getElementById("fields");
    var contentSec = document.getElementById("content-sec");
    var contentInput = document.getElementById("content-input");
    var contentCheck = document.getElementById("content-check");
    var contentVerdict = document.getElementById("content-verdict");
    var apiInput = document.getElementById("apiurl");
    var rawEl = document.getElementById("rawjson");
    function setPill(text, cls) {
        pill.textContent = text;
        pill.className = "pill " + cls;
    }
    function copyText(text, btn) {
        function done(ok) {
            if (!btn)
                return;
            btn.disabled = false;
            btn.textContent = ok ? "Đã copy" : "Lỗi copy";
            setTimeout(function () { btn.textContent = "Copy"; }, 1500);
        }
        if (navigator.clipboard && navigator.clipboard.writeText) {
            navigator.clipboard.writeText(text).then(function () { done(true); }, function () { done(false); });
            return;
        }
        try {
            var ta = document.createElement("textarea");
            ta.value = text;
            ta.style.position = "fixed";
            ta.style.opacity = "0";
            document.body.appendChild(ta);
            ta.select();
            var ok = document.execCommand("copy");
            document.body.removeChild(ta);
            done(ok);
        }
        catch (e) {
            done(false);
        }
    }
    function addCopy(container, text) {
        var btn = document.createElement("button");
        btn.type = "button";
        btn.textContent = "Copy";
        btn.addEventListener("click", function () {
            btn.disabled = true;
            copyText(text, btn);
        });
        container.appendChild(btn);
    }
    // badgeColor from the API is an ANSI SGR string ("38;2;r;g;bm").
    // Anything else falls back to the verdict colors.
    function badgeCss(color, valid) {
        var m = /^38;2;(\d{1,3});(\d{1,3});(\d{1,3})m$/.exec((color || "").trim());
        if (m) {
            var c = [m[1], m[2], m[3]].map(function (v) { return Math.max(0, Math.min(255, +v)); });
            return "rgb(" + c.join(",") + ")";
        }
        return valid ? "#4f7dff" : "#ff6b6b";
    }
    function addField(label, value, copyable) {
        var row = document.createElement("div");
        row.className = "row";
        var k = document.createElement("div");
        k.className = "k";
        k.textContent = label;
        var v = document.createElement("div");
        v.className = "v";
        v.textContent = value;
        row.appendChild(k);
        row.appendChild(v);
        if (copyable !== false)
            addCopy(row, value);
        fieldsEl.appendChild(row);
        return v;
    }
    function fail(message) {
        setPill(message, "bad");
    }
    try {
        if (!window.isSecureContext) {
            document.getElementById("insecure").classList.remove("hidden");
        }
    }
    catch (e) { /* banner is advisory; never block verification */ }
    apiInput.value = location.href;
    document.getElementById("copy-api").addEventListener("click", function () {
        var btn = this;
        btn.disabled = true;
        copyText(apiInput.value, btn);
    });
    var api = location.pathname + location.search;
    gateFetch(api, { headers: { "Accept": "application/json" }, cache: "no-store" })
        .then(function (r) { return r.json().then(function (j) { return { status: r.status, body: j }; }); })
        .then(function (res) {
        var j = res.body || {};
        rawEl.textContent = JSON.stringify(j, null, 2);
        if (!j.valid) {
            fail("❌ Không hợp lệ" + (j.error ? ": " + j.error : ""));
            if (j.error)
                addField("lỗi", String(j.error), false);
            return;
        }
        setPill("✅ Trip hợp lệ", "ok");
        var q = new URLSearchParams(location.search);
        var wantHash = (q.get("msg_hash") || "").toLowerCase();
        if (wantHash) {
            contentSec.classList.remove("hidden");
            contentCheck.addEventListener("click", checkContent);
            contentInput.addEventListener("input", checkContent);
        }
        function hex(buf) {
            return Array.prototype.map.call(new Uint8Array(buf), function (b) {
                return ("0" + b.toString(16)).slice(-2);
            }).join("");
        }
        function checkContent() {
            var t = contentInput.value;
            if (!t) {
                contentVerdict.textContent = "Chưa dán nội dung — mới chỉ xác thực chữ ký.";
                contentVerdict.className = "";
                return;
            }
            if (!window.crypto || !crypto.subtle) {
                contentVerdict.textContent = "Trình duyệt thiếu WebCrypto (cần HTTPS/localhost).";
                contentVerdict.className = "bad";
                return;
            }
            crypto.subtle.digest("SHA-256", new TextEncoder().encode(t)).then(function (buf) {
                if (hex(buf) === wantHash) {
                    contentVerdict.textContent = "khớp ✓ Nội dung đúng tin đã ký.";
                    contentVerdict.className = "ok";
                }
                else {
                    contentVerdict.textContent = "lệch ✗ Nội dung không khớp hash đã ký.";
                    contentVerdict.className = "bad";
                }
            }, function () {
                contentVerdict.textContent = "Không băm được nội dung.";
                contentVerdict.className = "bad";
            });
        }
        // Field order and names mirror the client's /info block so the
        // two can be read side by side; each label keeps the original
        // key in parentheses. Copy buttons always take the raw value.
        // height and sent_at are display context carried in the URL and
        // are NOT covered by the signature or the chain hash, so editing
        // them changes nothing about the verdict — their labels say so.
        var height = q.get("height") || "";
        if (height)
            addField("Chiều cao (height, không xác minh)", height, true);
        var tmp = q.get("tmp_id") || "";
        if (tmp)
            addField("ID phiền (tmp_id)", tmp, true);
        var rp = q.get("reply_to") || "";
        if (rp)
            addField("Trích dẫn (reply_to)", rp, true);
        var sentAt = q.get("sent_at") || "";
        if (sentAt)
            addField("Thời gian gửi (sent_at, không xác minh)", sentAt, true);
        var dn = q.get("display_name") || "";
        if (dn)
            addField("Tên (from)", dn, true);
        if (j.badge) {
            var badgeEl = addField("Chữ ký trip (trip)", j.badge, true);
            badgeEl.style.color = badgeCss(j.badgeColor, true);
            badgeEl.style.fontWeight = "700";
        }
        addField("Số thứ tự (trip.seq)", String(j.seq), true);
        if (j.pub)
            addField("Khoá (trip.pub)", j.pub, true);
        var prev = q.get("prev") || "";
        if (prev)
            addField("Chuỗi trước (trip.prev)", prev, true);
        var sig = q.get("sig") || "";
        if (sig)
            addField("Chữ ký (trip.sig)", sig, true);
        var msgHash = q.get("msg_hash") || "";
        if (msgHash)
            addField("Hash nội dung (trip.hash)", msgHash, true);
        if (j.server_pub)
            addField("Khoá server (trip.srv)", j.server_pub, true);
    })
        .catch(function (e) {
        fail("❌ Không gọi được API: " + (e && e.message ? e.message : e));
    });
})();
