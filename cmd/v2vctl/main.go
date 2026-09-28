package main

// v2vctl is the identity & server management tool split out of the chat
// client and the server.
//
// Layout mirrors real CLIs: `keygen` is a parent command with one leaf per
// identity flavor, so each flavor owns exactly the flags it needs.
//
// Interactive TTY sessions drive huh forms; scripts stay strictly
// flag-driven (defaults apply, hard errors on missing required values).
// The tool only touches local files: key.json, roles.json (opt-in merge)
// and the WEBAUTHN_STORE. No network, no daemon.

import (
	"github.com/alecthomas/kong"
)

var Version = "dev"

type CLI struct {
	Root     string      `help:"Thư mục instance (mặc định instances/default)" default:"instances/default" env:"V2V_ROOT" short:"R"`
	Keygen   KeygenCmd   `cmd:"" aliases:"kg" help:"Tạo danh tính cá nhân vào key.json"`
	Role     RoleCmd     `cmd:"" aliases:"r" help:"Quản lý role trong roles.json"`
	Enroll   EnrollCmd   `cmd:"" aliases:"en" help:"Phát ticket enroll passkey thật (chạy trên host server)"`
	List     ListCmd     `cmd:"" aliases:"ls" help:"Xem tickets và credentials trong store"`
	Migrate  MigrateCmd  `cmd:"" aliases:"mg" help:"Đổi preset mã hóa cho key.json hiện có"`
	Config   ConfigCmd   `cmd:"" aliases:"c" help:"Đồng bộ config theo template manifest (v2v-template.json)"`
	Instance InstanceCmd `cmd:"" aliases:"i" help:"Quản lý instance (deploy nhiều môi trường)"`
}

type KeygenCmd struct {
	Ed25519 Ed25519Keygen `cmd:"" name:"ed25519" aliases:"ed" help:"Danh tính ed25519 key-file"`
}

var cli CLI

func main() {
	ctx := kong.Parse(&cli)
	SetRoot(cli.Root)
	ctx.FatalIfErrorf(ctx.Run())
}
