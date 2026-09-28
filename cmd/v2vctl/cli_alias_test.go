package main

import (
	"testing"

	"github.com/alecthomas/kong"
)

func parseArgs(t *testing.T, args []string) (CLI, *kong.Context) {
	t.Helper()
	var parsed CLI
	parser, err := kong.New(&parsed)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}
	ctx, err := parser.Parse(args)
	if err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return parsed, ctx
}

func TestCommandAliasesResolveToCanonical(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"keygen", []string{"kg", "ed"}, "keygen ed25519"},
		{"role create", []string{"r", "new", "ops"}, "role create <role>"},
		{"role list", []string{"r", "ls"}, "role list"},
		{"role show", []string{"r", "sh", "ops"}, "role show <role>"},
		{"role update", []string{"r", "set", "ops"}, "role update <role>"},
		{"role delete", []string{"r", "rm", "ops"}, "role delete <role>"},
		{"role add-identity", []string{"r", "add", "ops"}, "role add-identity <role>"},
		{"role import", []string{"r", "imp"}, "role import"},
		{"enroll", []string{"en"}, "enroll"},
		{"list", []string{"ls"}, "list"},
		{"migrate", []string{"mg"}, "migrate"},
		{"config sync", []string{"c", "sy"}, "config sync"},
		{"config diff", []string{"c", "df"}, "config diff"},
		{"config manifest", []string{"c", "mf"}, "config manifest"},
		{"config check", []string{"c", "ck"}, "config check"},
		{"config take", []string{"c", "tk"}, "config take"},
		{"config validate", []string{"c", "val"}, "config validate"},
		{"instance init", []string{"i", "new", "prod"}, "instance init <name>"},
		{"instance list", []string{"i", "ls"}, "instance list"},
		{"instance status", []string{"i", "st"}, "instance status"},
		{"instance up", []string{"i", "up", "prod"}, "instance up <name>"},
		{"instance down", []string{"i", "dn", "prod"}, "instance down <name>"},
		{"instance restart", []string{"i", "rs", "prod"}, "instance restart <name>"},
		{"instance logs", []string{"i", "log", "prod"}, "instance logs <name>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ctx := parseArgs(t, tc.args)
			if got := ctx.Command(); got != tc.want {
				t.Fatalf("Command() = %q, muốn %q", got, tc.want)
			}
		})
	}
}

func TestCanonicalNamesStillWork(t *testing.T) {
	_, ctx := parseArgs(t, []string{"instance", "restart", "prod"})
	if got := ctx.Command(); got != "instance restart <name>" {
		t.Fatalf("Command() = %q, muốn %q", got, "instance restart <name>")
	}
}

func TestUnknownCommandRejected(t *testing.T) {
	var parsed CLI
	parser, err := kong.New(&parsed)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}
	for _, args := range [][]string{{"zzz"}, {"role", "zzz"}, {"i", "zzz"}} {
		if _, err := parser.Parse(args); err == nil {
			t.Fatalf("parse %v: muốn lỗi nhưng thành công", args)
		}
	}
}

func TestShortFlagsBind(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		parsed, _ := parseArgs(t, []string{"-R", "instances/x", "r", "ls"})
		if parsed.Root != "instances/x" {
			t.Fatalf("Root = %q", parsed.Root)
		}
	})

	t.Run("enroll", func(t *testing.T) {
		parsed, _ := parseArgs(t, []string{"en", "-r", "admin", "-l", "dev", "-s", "/tmp/store"})
		e := parsed.Enroll
		if e.Role != "admin" || e.Label != "dev" || e.Store != "/tmp/store" {
			t.Fatalf("enroll = %+v", e)
		}
	})

	t.Run("migrate", func(t *testing.T) {
		parsed, _ := parseArgs(t, []string{"mg", "-p", "wasm", "-f"})
		m := parsed.Migrate
		if m.Preset != "wasm" || !m.Force {
			t.Fatalf("migrate = %+v", m)
		}
	})

	t.Run("role add-identity", func(t *testing.T) {
		parsed, _ := parseArgs(t, []string{"r", "add", "ops", "-k", "aa", "-H", "bb", "-S", "cc", "-p", "-f", "x", "-F"})
		a := parsed.Role.AddIdentity
		if a.Role != "ops" || a.PublicKey != "aa" || a.HmacShield != "bb" ||
			a.ServerPubKey != "cc" || !a.Paste || a.File != "x" || !a.Force {
			t.Fatalf("add-identity = %+v", a)
		}
	})

	t.Run("config sync", func(t *testing.T) {
		parsed, _ := parseArgs(t, []string{"c", "sy", "-o", "env", "-C", "cd", "-N", "-T", "env", "-n", "-f", "-q"})
		s := parsed.Config.Sync
		if s.Only != "env" || s.ClientDir != "cd" || !s.NoPager || len(s.PreferTemplate) != 1 ||
			s.PreferTemplate[0] != "env" || !s.DryRun || !s.Force || !s.Quiet {
			t.Fatalf("config sync = %+v", s)
		}
	})

	t.Run("config take", func(t *testing.T) {
		parsed, _ := parseArgs(t, []string{"c", "tk", "-l", "-f"})
		tk := parsed.Config.Take
		if !tk.List || !tk.Force {
			t.Fatalf("config take = %+v", tk)
		}
	})

	t.Run("instance init", func(t *testing.T) {
		parsed, _ := parseArgs(t, []string{"i", "new", "prod", "-p", "1000", "-b", "127.0.0.1"})
		in := parsed.Instance.Init
		if in.Port != 1000 || in.Bind != "127.0.0.1" {
			t.Fatalf("instance init = %+v", in)
		}
	})

	t.Run("instance up", func(t *testing.T) {
		parsed, _ := parseArgs(t, []string{"i", "up", "prod"})
		if !parsed.Instance.Up.Build {
			t.Fatalf("instance up Build = false, muốn true")
		}
	})

	t.Run("instance logs", func(t *testing.T) {
		parsed, _ := parseArgs(t, []string{"i", "log", "prod", "-f"})
		if !parsed.Instance.Logs.Follow {
			t.Fatalf("instance logs Follow = false, muốn true")
		}
	})
}
