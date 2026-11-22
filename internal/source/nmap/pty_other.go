//go:build !linux

package nmap

import (
	"errors"
	"os"
	"os/exec"
)

func openPTY() (master, slave *os.File, err error) { return nil, nil, errors.New("no pty support") }

func attachPTY(cmd *exec.Cmd, slave *os.File) { cmd.Stdin = slave }
