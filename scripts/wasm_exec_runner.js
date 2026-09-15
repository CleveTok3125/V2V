'use strict';
// Runner for Go js/wasm test binaries under node, used as:
//   GOOS=js GOARCH=wasm go test -exec "node scripts/wasm_exec_runner.js" ./client/
// Test binary path arrives as argv[2], remaining args forward to the
// test binary via go.argv (flags like -test.run). Needs node only;
// GOROOT's wasm_exec.js provides the Go bridge.
const fs = require('fs');
const path = require('path');
const { execSync } = require('child_process');

const goroot = execSync('go env GOROOT').toString().trim();
require(path.join(goroot, 'lib', 'wasm', 'wasm_exec.js'));

const go = new Go();
go.argv = process.argv.slice(2);
go.env = { ...process.env };
// Exit with Go's status at once: otherwise node lingers on pending
// timers (e.g. -test.timeout) and their late firing throws
// "Go program has already exited" after a green run.
go.exit = (code) => process.exit(code);

WebAssembly.instantiate(fs.readFileSync(process.argv[2]), go.importObject)
	.then((result) => go.run(result.instance))
	.catch((err) => {
		console.error(err);
		process.exit(1);
	});
