#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."

command -v go >/dev/null 2>&1 || { echo "go is not installed" >&2; exit 1; }
command -v cc >/dev/null 2>&1 || { echo "no C compiler (cc): the tree-sitter grammars need cgo" >&2; exit 1; }

go mod download
go build ./...

echo "Ready: devops agents test | lint | impact <args>"
