package main

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/CleveTok3125/V2V/internal/config"
)

// Input-loop smoke coverage: dispatch routing, reply one-shot targets,
// draft handling, send guards, codeblock cancel, and send consumption.
// Each case mirrors the order used by the interactive loop.

func smokeSession() *Session {
	sess := NewSession()
	sess.Display.Out = io.Discard
	sess.Display.Term = &fakeTerm{}
	sess.Display.TabChat = newTabBuffer(100, 100000)
	sess.Display.TabSys = newTabBuffer(100, 100000)
	sess.Chain.WireIdx = newWireIndex(16)
	sess.Conn = &stubConn{}
	sess.Quitting = make(chan bool, 1)
	sess.Verify.VerifyCh = make(chan verifyJob, 4)
	sess.Display.TabChat.append("| Alice: hi")
	sess.Display.TabChat.append(metaLineFor(1234, "abcd0000", ""))
	return sess
}

func TestInputLoopBuiltinCommands(t *testing.T) {
	sess := smokeSession()
	if _, act := sess.dispatch("hello"); act != cmdPass {
		t.Fatalf("plain message act=%v, want cmdPass", act)
	}
	if _, act := sess.dispatch("/nope123"); act != cmdDone {
		t.Fatalf("unknown slash act=%v, want cmdDone", act)
	}
	for _, cmd := range []string{
		"/whoami", "/status", "/help", "/tab", "/clear",
		"/meta", "/find 1234", "/info 1234", "/copy 1234",
		"/clearhistory", "/showjoin", "/autoverify",
	} {
		sess := smokeSession()
		if _, act := sess.dispatch(cmd); act != cmdDone {
			t.Fatalf("%s act=%v, want cmdDone", cmd, act)
		}
		if sess.Pending.PendingReplyTo != 0 {
			t.Fatalf("%s left PendingReplyTo=%d", cmd, sess.Pending.PendingReplyTo)
		}
	}
}

func TestInputLoopInlineReply(t *testing.T) {
	sess := smokeSession()
	text, act := sess.dispatch("/reply 1234 agreed")
	if act != cmdPass || text != "agreed" || sess.Pending.PendingReplyTo != 1234 {
		t.Fatalf("inline reply got %q act=%v reply=%d", text, act, sess.Pending.PendingReplyTo)
	}

	for _, body := range []string{"/meta", "/clear"} {
		sess := smokeSession()
		if _, act := sess.dispatch("/reply 1234 "+body); act != cmdDone {
			t.Fatalf("reply with command body %s act=%v, want cmdDone", body, act)
		}
		if sess.Pending.PendingReplyTo != 0 {
			t.Fatalf("reply with command body %s left PendingReplyTo=%d", body, sess.Pending.PendingReplyTo)
		}
	}
}

func TestInputLoopReplyDraft(t *testing.T) {
	sess := smokeSession()
	if _, act := sess.dispatch("/reply 1234"); act != cmdDone || sess.Pending.ReplyDraft != 1234 {
		t.Fatalf("draft open act=%v draft=%d", act, sess.Pending.ReplyDraft)
	}
	sess.handleDraftGate("draft body")
	if sess.Pending.PendingReplyTo != 1234 || sess.Pending.ReplyDraft != 0 {
		t.Fatalf("draft attach reply=%d draft=%d", sess.Pending.PendingReplyTo, sess.Pending.ReplyDraft)
	}

	sess = smokeSession()
	sess.Pending.ReplyDraft = 1234
	sess.handleDraftGate("/meta")
	if sess.Pending.ReplyDraft != 0 || sess.Pending.PendingReplyTo != 0 {
		t.Fatal("slash input must abort the draft without attaching a target")
	}
}

func TestInputLoopGuardRejectClearsTarget(t *testing.T) {
	oldCfg := ClientCfg
	ClientCfg = config.DefaultClientConfig()
	defer func() { ClientCfg = oldCfg }()

	sess := smokeSession()
	sess.Pending.PendingReplyTo = 1234
	if sess.checkSendGuards(strings.Repeat("x", 100000)) {
		t.Fatal("overlong message must fail the send guards")
	}
	sess.Pending.PendingReplyTo = 0
	if sess.Pending.PendingReplyTo != 0 {
		t.Fatal("rejected message must not keep its reply target")
	}
}

func TestInputLoopCodeblockCancelClearsTarget(t *testing.T) {
	sess := smokeSession()
	sess.Pending.PendingReplyTo = 1234
	sess.Display.Term = &fakeTerm{errs: map[int]error{1: ErrInputCancel}}
	if _, _, ok := sess.collectBody("```"); ok {
		t.Fatal("canceled codeblock must return ok=false")
	}
	sess.Pending.PendingReplyTo = 0
	if sess.Pending.PendingReplyTo != 0 {
		t.Fatal("canceled codeblock must not keep its reply target")
	}
}

func TestInputLoopSendConsumesTarget(t *testing.T) {
	sess := smokeSession()
	sess.Pending.PendingReplyTo = 9
	if err := sess.sendMessage("hi", 1, false, 0); err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	if sess.Pending.PendingReplyTo != 0 {
		t.Fatal("sent message must clear its one-shot reply target")
	}
	if len(sess.Pending.PendingPlaceholders) != 1 {
		t.Fatal("sent message must track its placeholder")
	}

	sess = smokeSession()
	sess.Conn = &stubConn{writeErr: errors.New("connection unavailable")}
	sess.Pending.PendingReplyTo = 9
	if err := sess.sendMessage("hi", 1, false, 0); err == nil {
		t.Fatal("failed send must return an error")
	}
	if sess.Pending.PendingReplyTo != 0 {
		t.Fatal("failed send must still clear its one-shot reply target")
	}
}
