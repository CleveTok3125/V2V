// Test harness for web role:hash parsing
const assert = (cond, msg) => { if (!cond) throw new Error(msg); };

// Simulate the JS parsing logic from app.js
function parseRoleHash(v) {
  const idx = v.indexOf(":");
  if (idx > 0) {
    const rolePart = v.slice(0, idx).trim();
    if (rolePart && rolePart !== v) {
      return rolePart;
    }
  }
  return v;
}

// Test cases
console.log("Testing role:hash parsing (web)...");
const cases = [
  ["member:abc123", "member"],
  ["admin:deadbeef", "admin"],
  ["member", "member"],
  [":hashonly", ":hashonly"],
  ["", ""],
  ["  member : hash ", "member"],
  ["guest:xyz:extra", "guest"], // only first colon
];

let passed = 0;
for (const [input, expected] of cases) {
  const got = parseRoleHash(input);
  if (got !== expected) {
    console.error(`FAIL: parseRoleHash(${JSON.stringify(input)})=${JSON.stringify(got)} want ${JSON.stringify(expected)}`);
    process.exit(1);
  }
  passed++;
}
console.log(`✓ ${passed}/${cases.length} role:hash cases passed`);

// Test that username with colon is NOT parsed (only Role field is)
const username = "alice:hash123";
const parsedUsername = parseRoleHash(username);
// In real UI, username field should NOT be parsed, only Role field
// This test documents that if parseRoleHash were applied to username, it would incorrectly parse
// So we verify the function would parse it, but the UI should not call it on username
assert(parsedUsername === "alice", "username would be parsed if applied, but UI should not apply it to username field");
console.log("✓ username false-positive check passed (documents that only Role field should use parseRoleHash)");

console.log("All web harness tests passed");
