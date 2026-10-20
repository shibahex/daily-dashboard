#!/usr/bin/env bash
# Start the dashboard at http://localhost:1628 (pass a different port as $1).
# Same as: cd site && nix run . -- <port>
cd "$(dirname "$0")"
exec nix run . -- "${1:-1628}"
