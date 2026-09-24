// Ambient declarations for the webterm globals: the Go-generated
// wasm_exec.js exposes Go, xterm.js exposes Terminal, and the Go WASM
// client reads/writes the v2v* bridge on window.

declare var Go: any;
declare var Terminal: any;

interface Window {
  V2V_VERSION?: string;
  v2vSendKeys?: (s: string) => void;
  v2vOutput?: (s: string) => void;
  v2vConfig?: any;
  v2vSetStatus?: (msg: string, isError: boolean) => void;
  v2vSetSize?: (cols: number, rows: number) => void;
  v2vRefresh?: () => void;
  v2vRequestAssertion?: (nonceHex: string, role: string) => void;
  v2vAssertionReady?: (payload: string) => void;
  v2vExit?: () => void;
  v2vSolvePow?: (paramsJSON: string, cb: (err: string | null, nonce: number | null) => void) => void;
  v2vArgon2?: (paramsJSON: string, cb: (err: string | null, keyHex: string | null) => void) => void;
}
