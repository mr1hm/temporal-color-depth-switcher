//go:build windows

package main

import (
	"os"
	"syscall"
)

func redirectStderr(f *os.File) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	setStdHandle := kernel32.NewProc("SetStdHandle")
	const stdErrorHandle = ^uintptr(0) - 12 + 1 // STD_ERROR_HANDLE = -12
	setStdHandle.Call(stdErrorHandle, uintptr(f.Fd()))
	os.Stderr = f
}
