// Ambient declarations for the webterm globals: the Go-generated
// wasm_exec.js exposes Go, xterm.js exposes Terminal, and the Go WASM
// client reads/writes the v2v* bridge on window.

declare var Go: any;
declare var Terminal: any;

// What the page hands the Go WASM client at startup. parseFlags in
// client/config_wasm.go reads it: showJoin/username/serverUrl/tripcode
// drive the connection, the rest map onto the compiled client config
// (ui.meta.show, defaults.autoVerify, ui.notify.*). Every field is
// optional — an absent one keeps the client's default, while a present
// `false` is honored.
interface V2VConfig {
  serverUrl?: string;
  username?: string;
  tripcode?: string;
  showJoin?: boolean;
  passkey?: boolean;
  passkeyRole?: string;
  showMeta?: boolean;
  autoVerify?: boolean;
  notify?: {
    pow?: boolean;
    powMinTier?: number;
    history?: boolean;
    join?: boolean;
    date?: boolean;
    system?: boolean;
  };
}

interface Window {
  V2V_VERSION?: string;
  v2vSendKeys?: (s: string) => void;
  v2vOutput?: (s: string) => void;
  v2vConfig?: V2VConfig;
  v2vSetStatus?: (msg: string, isError: boolean) => void;
  v2vSetSize?: (cols: number, rows: number) => void;
  v2vRefresh?: () => void;
  v2vRequestAssertion?: (nonceHex: string, role: string) => void;
  v2vAssertionReady?: (payload: string) => void;
  v2vExit?: () => void;
  v2vSolvePow?: (paramsJSON: string, cb: (err: string | null, nonce: number | null) => void) => void;
  v2vArgon2?: (paramsJSON: string, cb: (err: string | null, keyHex: string | null) => void) => void;
}
