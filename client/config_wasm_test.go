//go:build js

package main

import (
	"syscall/js"
	"testing"

	"github.com/CleveTok3125/V2V/internal/config"
)

// installWebCfg writes a window.v2vConfig carrying only the listed notify
// gates plus the scalar options, and restores the compiled defaults after
// the test: ClientCfg is process-wide state, not per-test state.
func installWebCfg(t *testing.T, showMeta, autoVerify *bool, notify map[string]any) {
	t.Helper()
	cfg := js.Global().Get("Object").New()
	if showMeta != nil {
		cfg.Set("showMeta", *showMeta)
	}
	if autoVerify != nil {
		cfg.Set("autoVerify", *autoVerify)
	}
	if notify != nil {
		obj := js.Global().Get("Object").New()
		for k, v := range notify {
			obj.Set(k, v)
		}
		cfg.Set("notify", obj)
	}
	js.Global().Set("v2vConfig", cfg)
	t.Cleanup(func() {
		js.Global().Delete("v2vConfig")
		ClientCfg = config.DefaultClientConfig()
	})
	// ClientCfg is process state, so each install starts from the compiled
	// defaults; a test may call this more than once to compare payloads.
	ClientCfg = config.DefaultClientConfig()
	applyWebClientOpts(cfg)
}

func boolp(b bool) *bool { return &b }

func TestWasmWebOptsAbsentKeepsDefaults(t *testing.T) {
	installWebCfg(t, nil, nil, nil)

	if !ClientCfg.ShowMeta() {
		t.Fatal("absent showMeta must keep the default (shown)")
	}
	if !ClientCfg.DefaultAutoVerify() {
		t.Fatal("absent autoVerify must keep the default (enabled)")
	}
	if !ClientCfg.NotifyPow() || !ClientCfg.NotifyHistory() || !ClientCfg.NotifyJoin() ||
		!ClientCfg.NotifyDate() || !ClientCfg.NotifySystem() {
		t.Fatal("absent notify gates must keep the defaults (shown)")
	}
	if got := ClientCfg.NotifyPowMinTier(); got != 1 {
		t.Fatalf("absent powMinTier = %d, want 1", got)
	}
}

func TestWasmWebOptsExplicitFalseWins(t *testing.T) {
	// The whole point of the presence check: JS false is falsy, so a
	// truthiness test would read these as absent and leave the defaults on.
	installWebCfg(t, boolp(false), boolp(false), map[string]any{
		"pow":     false,
		"history": false,
		"join":    false,
		"date":    false,
		"system":  false,
	})

	if ClientCfg.ShowMeta() {
		t.Fatal("showMeta=false must turn meta off")
	}
	if ClientCfg.DefaultAutoVerify() {
		t.Fatal("autoVerify=false must turn auto-verify off")
	}
	if ClientCfg.NotifyPow() || ClientCfg.NotifyHistory() || ClientCfg.NotifyJoin() ||
		ClientCfg.NotifyDate() || ClientCfg.NotifySystem() {
		t.Fatal("notify=false must mute every gate")
	}
}

func TestWasmWebOptsExplicitTrueWins(t *testing.T) {
	installWebCfg(t, boolp(true), boolp(true), map[string]any{"pow": true, "join": true})
	// Turn everything off behind the page's back, then replay the same
	// payload: a present true has to actively restore each switch.
	off := false
	ClientCfg.UI.Meta.Show = &off
	ClientCfg.Defaults.AutoVerify = &off
	ClientCfg.UI.Notify.Pow = &off
	ClientCfg.UI.Notify.Join = &off
	applyWebClientOpts(js.Global().Get("v2vConfig"))

	if !ClientCfg.ShowMeta() || !ClientCfg.DefaultAutoVerify() {
		t.Fatal("explicit true must be honored")
	}
	if !ClientCfg.NotifyPow() || !ClientCfg.NotifyJoin() {
		t.Fatal("explicit notify true must be honored")
	}
}

// A value of the wrong type is not a setting: it must fall through to the
// compiled default rather than being coerced, and a non-object notify must
// not panic.
func TestWasmWebOptsWrongTypeFallsBack(t *testing.T) {
	notify := js.Global().Get("Object").New()
	notify.Set("pow", "yes")
	notify.Set("powMinTier", "3")

	cases := []js.Value{js.ValueOf("x"), js.ValueOf(1), js.Null(), notify, js.Undefined()}
	fields := []string{"showMeta", "autoVerify", "notify", "showMeta", "notify"}
	for i, bad := range cases {
		cfg := js.Global().Get("Object").New()
		cfg.Set(fields[i], bad)
		applyWebClientOpts(cfg)

		if !ClientCfg.ShowMeta() || !ClientCfg.DefaultAutoVerify() ||
			!ClientCfg.NotifyPow() || ClientCfg.NotifyPowMinTier() != 1 {
			t.Fatalf("%s=%#v must leave the compiled defaults in place", fields[i], bad)
		}
		ClientCfg = config.DefaultClientConfig()
	}
}

func TestWasmWebOptsPowMinTier(t *testing.T) {
	installWebCfg(t, nil, nil, map[string]any{"powMinTier": 3})
	if got := ClientCfg.NotifyPowMinTier(); got != 3 {
		t.Fatalf("powMinTier = %d, want 3", got)
	}

	// Nonsense keeps the default instead of silencing the notice.
	installWebCfg(t, nil, nil, map[string]any{"powMinTier": 0})
	if got := ClientCfg.NotifyPowMinTier(); got != 1 {
		t.Fatalf("powMinTier 0 = %d, want the default 1", got)
	}
}
