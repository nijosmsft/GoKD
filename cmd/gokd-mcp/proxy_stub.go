//go:build !remote

package main

import (
	"errors"
	"io"
)

// runRemoteProxy is a stub used in default builds. The real implementation
// lives in proxy.go behind the `remote` build tag, which pulls in
// github.com/nijosmsft/lablink. Until lablink ships a tagged release, the
// remote proxy is only available in dev builds:
//
//	go build -tags remote ./cmd/gokd-mcp
//
// with C:\git\lablink (or the path named by go.mod's replace directive)
// present.
func runRemoteProxy(_ config, _ io.Writer) error {
	return errors.New("-remote requires building with `-tags remote`; see cmd/gokd-mcp/proxy.go")
}
