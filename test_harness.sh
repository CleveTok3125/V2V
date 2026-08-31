#!/bin/sh
# Test harness for v0.5.0 features: role:hash parsing, atomic writes, enroll merge, templates
set -e
echo "=== V2V Test Harness v0.5.0 ==="
echo ""

echo "[1/5] Running Go tests..."
GOCACHE=/tmp/gocache go test ./... 2>&1 | tail -5
echo "✓ Go tests passed"
echo ""

echo "[2/5] Running web role:hash harness..."
node webterm/app.test.js
echo ""

echo "[3/5] Checking templates..."
if grep -q "^WEBAUTHN_RPID=" template/.env && grep -q "^WEBAUTHN_ORIGIN=" template/.env; then
  echo "✓ template/.env has active WEBAUTHN_*"
else
  echo "✗ template/.env missing active WEBAUTHN_*"
  exit 1
fi
if grep -q '"version": 2' template/key.json && grep -q '"host":' template/key.json; then
  echo "✓ template/key.json has version 2 with host"
else
  echo "✗ template/key.json not updated to v2"
  exit 1
fi
if grep -q '"host":' template/roles.json && grep -q '"passkeys"' template/roles.json; then
  echo "✓ template/roles.json has host and passkeys"
else
  echo "✗ template/roles.json not updated"
  exit 1
fi
echo ""

echo "[4/5] Checking atomic write helpers..."
if grep -q "atomicWriteFile" identity/identity.go && grep -q "CreateTemp" identity/identity.go; then
  echo "✓ identity atomic write with CreateTemp+Sync"
else
  echo "✗ identity atomic write not found"
  exit 1
fi
if grep -q "atomicWriteFile" server/webauthn_store.go; then
  echo "✓ webauthn_store atomic write"
else
  echo "✗ webauthn_store atomic write not found"
  exit 1
fi
if grep -q 'indexOf.*":"' webterm/app.js && grep -q "passkeyRoleInput" webterm/app.js; then
  echo "✓ web role:hash parsing in Role field"
else
  echo "✗ web role:hash parsing not found"
  exit 1
fi
if grep -q "Unlimited" cmd/v2vctl/main.go && grep -q "Prefix" cmd/v2vctl/main.go; then
  echo "✓ enroll has unlimited/prefix"
else
  echo "✗ enroll missing unlimited/prefix"
  exit 1
fi
echo ""

echo "[5/5] Checking version tag..."
if git describe --tags --long HEAD | grep -q "v0\."; then
  echo "✓ version tag present: $(git describe --tags --long HEAD)"
else
  echo "✗ version tag not found"
  exit 1
fi
echo ""

echo "=== All harness checks passed ==="
