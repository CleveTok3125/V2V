//go:build js

package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"syscall/js"

	"github.com/CleveTok3125/V2V/internal/pow"
)

// callBridge invokes a window.v2v* bridge function that uses the
// callback style (err, result). Receiving on the channel parks the Go
// runtime, which returns control to the JS event loop, so the worker
// finishes without freezing the page.
func callBridge(name, paramsJSON string) (js.Value, error) {
	fn := js.Global().Get(name)
	if !fn.Truthy() {
		return js.Undefined(), errors.New("pow bridge unavailable")
	}
	resCh := make(chan js.Value, 1)
	errCh := make(chan error, 1)
	cb := js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) < 2 {
			errCh <- errors.New("pow bridge: bad callback")
			return nil
		}
		if e := args[0]; !e.IsNull() && !e.IsUndefined() && e.String() != "" {
			errCh <- errors.New(e.String())
			return nil
		}
		resCh <- args[1]
		return nil
	})
	defer cb.Release()
	fn.Invoke(paramsJSON, cb)
	select {
	case v := <-resCh:
		return v, nil
	case e := <-errCh:
		return js.Undefined(), e
	}
}

// platformArgon2 offloads argon2id to the PoW worker on the wasm build,
// so tripcode derivation never freezes the browser main thread.
func platformArgon2(pass, salt []byte, t, m uint32, threads uint8) []byte {
	params, _ := json.Marshal(map[string]any{
		"passHex": hex.EncodeToString(pass),
		"saltHex": hex.EncodeToString(salt),
		"t":       t,
		"m":       m,
		"p":       threads,
		"dkLen":   32,
	})
	v, err := callBridge("v2vArgon2", string(params))
	if err != nil {
		return nil
	}
	out, _ := hex.DecodeString(v.String())
	return out
}

// platformSolvePoW offloads the search to the worker; the worker
// enforces the same wall-clock budget.
func platformSolvePoW(p pow.Preset, salt string, maxCostMs int64) (uint64, error) {
	params, _ := json.Marshal(map[string]any{
		"salt":       salt,
		"t":          p.Time,
		"m":          p.Memory,
		"p":          p.Threads,
		"difficulty": p.Difficulty,
		"maxMs":      maxCostMs,
	})
	v, err := callBridge("v2vSolvePow", string(params))
	if err != nil {
		return 0, err
	}
	return uint64(v.Float()), nil
}
