// Main-thread bridge to the PoW worker plus the join-gate helper. Both
// the webterm UI (enroll/verify fetches) and the Go WASM client call
// through here, so proof-of-work never runs on the main thread.
let worker = null;
let seq = 0;
const pending = new Map();
function ensureWorker() {
    if (worker)
        return worker;
    worker = new Worker("/web/pow_worker.js", { type: "module" });
    worker.onmessage = (ev) => {
        const m = ev.data || {};
        const p = pending.get(m.id);
        if (!p)
            return;
        pending.delete(m.id);
        if (m.ok)
            p.resolve(m.result);
        else
            p.reject(new Error(m.error || "worker error"));
    };
    worker.onerror = () => {
        for (const p of pending.values())
            p.reject(new Error("worker failed to load"));
        pending.clear();
    };
    return worker;
}
function call(msg) {
    const id = ++seq;
    return new Promise((resolve, reject) => {
        pending.set(id, { resolve, reject });
        ensureWorker().postMessage(Object.assign({ id }, msg));
    });
}
export function solvePow(p) {
    return call(Object.assign({ op: "pow" }, p));
}
export function argon2Hex(p) {
    return call(Object.assign({ op: "argon2" }, p));
}
// window bridges the Go WASM client calls (callback style, no Promise
// across the syscall/js boundary).
window.v2vSolvePow = (paramsJSON, cb) => {
    try {
        solvePow(JSON.parse(paramsJSON)).then((n) => cb(null, n), (e) => cb(String(e && e.message ? e.message : e), null));
    }
    catch (e) {
        cb(String(e), null);
    }
};
window.v2vArgon2 = (paramsJSON, cb) => {
    try {
        argon2Hex(JSON.parse(paramsJSON)).then((h) => cb(null, h), (e) => cb(String(e && e.message ? e.message : e), null));
    }
    catch (e) {
        cb(String(e), null);
    }
};
function sleep(ms) {
    return new Promise((r) => setTimeout(r, ms));
}
async function postGate(body) {
    const resp = await fetch("/api/join-gate", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
    });
    let json = null;
    try {
        json = await resp.json();
    }
    catch (_e) {
        json = null;
    }
    return { status: resp.status, json };
}
// acquireGatePass returns a lease pass when the server demands one, or
// null when the gate is not required / the offer is beyond the local
// budget. Callers then retry their request with the X-V2V-Pass header.
export async function acquireGatePass(onStatus) {
    const say = onStatus || (() => { });
    const st = await postGate({ step: "status", ui: "web" });
    if (st.status !== 200 || !st.json || !st.json.required)
        return null;
    const ch = await postGate({ step: "challenge", ui: "web" });
    if (ch.status !== 200 || !ch.json)
        return null;
    const c = {
        id: ch.json.challenge_id,
        tier: ch.json.tier || 0,
        t: ch.json.pow ? ch.json.pow.t : 0,
        m: ch.json.pow ? ch.json.pow.m : 0,
        p: ch.json.pow ? ch.json.pow.p : 1,
        difficulty: ch.json.pow ? ch.json.pow.difficulty : 0,
        salt: ch.json.salt,
        earliest: ch.json.earliest || 0,
        ticket: ch.json.ticket,
    };
    if (c.tier > 3) {
        say("Từ chối PoW tier " + c.tier + " (vượt cap).");
        return null;
    }
    say("Đang giải PoW (tier " + c.tier + ")…");
    let nonce;
    try {
        nonce = await solvePow({
            salt: c.salt,
            t: c.t,
            m: c.m,
            p: c.p,
            difficulty: c.difficulty,
            maxMs: 120000,
        });
    }
    catch (e) {
        say("Không giải được PoW: " + e);
        return null;
    }
    for (let attempt = 0; attempt < 2; attempt++) {
        const wait = c.earliest * 1000 - Date.now();
        if (wait > 0) {
            say("Đang chờ cửa vào…");
            await sleep(wait);
        }
        const res = await postGate({ step: "submit", challenge_id: c.id, nonce, ticket: c.ticket });
        if (res.status === 200 && res.json && res.json.pass)
            return res.json.pass;
        if (res.status === 429 && res.json && res.json.retry_after && attempt === 0) {
            await sleep(res.json.retry_after * 1000);
            continue;
        }
        return null;
    }
    return null;
}
// gateFetch wraps fetch with one gate retry: passkey/enroll and the
// verify page use it so heavy endpoints stay reachable under attack.
export async function gateFetch(input, init, onStatus) {
    const resp = await fetch(input, init);
    if (resp.status !== 429)
        return resp;
    const pass = await acquireGatePass(onStatus);
    if (!pass)
        return resp;
    const headers = new Headers(init && init.headers ? init.headers : undefined);
    headers.set("X-V2V-Pass", pass);
    return fetch(input, Object.assign({}, init, { headers }));
}
