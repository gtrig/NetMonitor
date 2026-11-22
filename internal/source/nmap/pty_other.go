//go:build !linux

package nmap

import (
	"errors"
	"os"
)

func openPTY() (master, slave *os.File, err error) { return nil, nil, errors.New("no pty support") }
