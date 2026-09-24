// Main-thread bridge to the PoW worker plus the join-gate helper. Both
// the webterm UI (enroll/verify fetches) and the Go WASM client call
// through here, so proof-of-work never runs on the main thread.

type Pending = {
  resolve: (v: any) => void;
  reject: (e: any) => void;
};

let worker: Worker | null = null;
let seq = 0;
const pending = new Map<number, Pending>();

function ensureWorker(): Worker {
  if (worker) return worker;
  worker = new Worker("/web/pow_worker.js", { type: "module" });
  worker.onmessage = (ev: MessageEvent) => {
    const m = ev.data || {};
    const p = pending.get(m.id);
    if (!p) return;
    pending.delete(m.id);
    if (m.ok) p.resolve(m.result);
    else p.reject(new Error(m.error || "worker error"));
  };
  worker.onerror = () => {
    for (const p of pending.values()) p.reject(new Error("worker failed to load"));
    pending.clear();
  };
  return worker;
}

function call<T>(msg: any): Promise<T> {
  const id = ++seq;
  return new Promise<T>((resolve, reject) => {
    pending.set(id, { resolve, reject });
    ensureWorker().postMessage(Object.assign({ id }, msg));
  });
}

export type SolveParams = {
  salt: string;
  t: number;
  m: number;
  p: number;
  difficulty: number;
  maxMs: number;
};

export function solvePow(p: SolveParams): Promise<number> {
  return call<number>(Object.assign({ op: "pow" }, p));
}

export type ArgonParams = {
  passHex: string;
  saltHex: string;
  t: number;
  m: number;
  p: number;
  dkLen: number;
};

export function argon2Hex(p: ArgonParams): Promise<string> {
  return call<string>(Object.assign({ op: "argon2" }, p));
}

// window bridges the Go WASM client calls (callback style, no Promise
// across the syscall/js boundary).
window.v2vSolvePow = (paramsJSON: string, cb: (err: string | null, nonce: number | null) => void) => {
  try {
    solvePow(JSON.parse(paramsJSON)).then(
      (n) => cb(null, n),
      (e) => cb(String(e && e.message ? e.message : e), null),
    );
  } catch (e) {
    cb(String(e), null);
  }
};

window.v2vArgon2 = (paramsJSON: string, cb: (err: string | null, keyHex: string | null) => void) => {
  try {
    argon2Hex(JSON.parse(paramsJSON)).then(
      (h) => cb(null, h),
      (e) => cb(String(e && e.message ? e.message : e), null),
    );
  } catch (e) {
    cb(String(e), null);
  }
};

type GateChallenge = {
  id: string;
  tier: number;
  t: number;
  m: number;
  p: number;
  difficulty: number;
  salt: string;
  earliest: number;
  ticket: string;
};

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}

async function postGate(body: any): Promise<{ status: number; json: any }> {
  const resp = await fetch("/api/join-gate", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  let json: any = null;
  try {
    json = await resp.json();
  } catch (_e) {
    json = null;
  }
  return { status: resp.status, json };
}

// acquireGatePass returns a lease pass when the server demands one, or
// null when the gate is not required / the offer is beyond the local
// budget. Callers then retry their request with the X-V2V-Pass header.
export async function acquireGatePass(onStatus?: (msg: string) => void): Promise<string | null> {
  const say = onStatus || (() => {});
  const st = await postGate({ step: "status", ui: "web" });
  if (st.status !== 200 || !st.json || !st.json.required) return null;

  const ch = await postGate({ step: "challenge", ui: "web" });
  if (ch.status !== 200 || !ch.json) return null;
  const c: GateChallenge = {
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
  let nonce: number;
  try {
    nonce = await solvePow({
      salt: c.salt,
      t: c.t,
      m: c.m,
      p: c.p,
      difficulty: c.difficulty,
      maxMs: 120000,
    });
  } catch (e) {
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
    if (res.status === 200 && res.json && res.json.pass) return res.json.pass;
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
export async function gateFetch(
  input: RequestInfo | URL,
  init?: RequestInit,
  onStatus?: (msg: string) => void,
): Promise<Response> {
  const resp = await fetch(input, init);
  if (resp.status !== 429) return resp;
  const pass = await acquireGatePass(onStatus);
  if (!pass) return resp;
  const headers = new Headers(init && init.headers ? init.headers : undefined);
  headers.set("X-V2V-Pass", pass);
  return fetch(input, Object.assign({}, init, { headers }));
}
