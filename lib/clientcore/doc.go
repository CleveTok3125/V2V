// Package clientcore is the reusable core of a V2V chat client: everything a
// client needs that is not a choice about its own interface.
//
// It carries the wire verdicts (VerifyWireContent, TripPayloadBytes), the
// notice-gate policy (Notices), the join-gate round (RunGateFlow and friends),
// the managed dial (Dialer.DialManaged behind Transport and Gate), proxy
// parsing, identity file loading, and the logging line shape.
//
// It deliberately carries none of the following, because each is a decision
// only the client can make:
//
//   - output: no printing, no terminal, no wording. Progress and failures
//     arrive as events (DialOutput.OnDial, GateOutput.OnGate, Prompter), and
//     the caller renders them.
//   - the runtime: the platform solver, the round tripper, the clipboard and
//     the key derivation function are all injected.
//   - process control: no os.Exit, and no dialling or reading a terminal on
//     its own.
//   - session and display state: tabs, entries, pending placeholders and the
//     reconnect loop belong to the client.
//
// A minimal client wires the three callbacks and lets the core do the rest:
//
//	d := clientcore.Dialer{
//		Transport: myTransport{},
//		Gate:      myGate{},
//		Config:    clientcore.DialConfig{KeyFile: "key", ReadLimit: 1 << 20, MaxAttempts: 3},
//		Output:    myOutput{},
//	}
//	conn, url, err := d.DialManaged(wsURL)
//
// where myTransport dials the socket, myGate obtains the join-gate lease pass
// and myOutput owns the wording of every event.
package clientcore
