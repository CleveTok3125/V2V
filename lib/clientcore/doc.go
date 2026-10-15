// Package clientcore holds the reusable core of a V2V chat client:
// wire verdicts, notice gates, the join-gate round, managed dialing,
// proxy parsing, identity loading and the log line shape.
//
// What it leaves out is a client decision: no printing or terminal,
// no platform solver or key derivation (all injected), no os.Exit,
// and no session or display state. Events arrive through callbacks
// (DialOutput.OnDial, GateOutput.OnGate, Prompter) for the caller
// to render.
//
//	d := clientcore.Dialer{
//		Transport: myTransport{},
//		Gate:      myGate{},
//		Config:    clientcore.DialConfig{KeyFile: "key", ReadLimit: 1 << 20, MaxAttempts: 3},
//		Output:    myOutput{},
//	}
//	conn, url, err := d.DialManaged(wsURL)
package clientcore
