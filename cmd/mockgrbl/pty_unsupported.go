//go:build !linux

package main

import (
	"fmt"
	"os"
	"runtime"
)

func openPTY() (*os.File, string, error) {
	return nil, "", fmt.Errorf("mockgrbl PTY endpoint is currently implemented only on linux, not %s", runtime.GOOS)
}
