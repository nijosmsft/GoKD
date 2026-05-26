#!/bin/bash
# Helper to invoke the Windows go.exe with MinGW on PATH from an MSYS2 bash shell.
# Usage: ./run_test.sh test -v ./...
#
# MSYS2 bash inherits the parent Windows environment, so USERPROFILE /
# LOCALAPPDATA are already set. We only re-export them in Windows-path form
# in case the user launched bash with them unset.
set -e
export PATH=/mingw64/bin:$PATH
: "${USERPROFILE:=$(cygpath -w "$HOME")}"
: "${LOCALAPPDATA:=${USERPROFILE}\\AppData\\Local}"
export USERPROFILE LOCALAPPDATA
export TEMP="${LOCALAPPDATA}\\Temp"
export TMP="$TEMP"
export GOCACHE="${LOCALAPPDATA}\\go-build"
export GOPATH="${USERPROFILE}\\go"
cd "$(dirname "$0")"
exec "/c/Program Files/Go/bin/go.exe" "$@"
