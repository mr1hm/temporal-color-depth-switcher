//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	modAdvapi32       = syscall.NewLazyDLL("advapi32.dll")
	procRegOpenKeyEx  = modAdvapi32.NewProc("RegOpenKeyExW")
	procRegSetValueEx = modAdvapi32.NewProc("RegSetValueExW")
	procRegDeleteVal  = modAdvapi32.NewProc("RegDeleteValueW")
	procRegQueryVal   = modAdvapi32.NewProc("RegQueryValueExW")
	procRegCloseKey   = modAdvapi32.NewProc("RegCloseKey")
)

const (
	hkeyLocalMachine = uintptr(0x80000002)
	keyWrite         = 0x20006
	keyRead          = 0x20019
	regSZ            = 1
)

const registryRunPath = `SOFTWARE\Wow6432Node\Microsoft\Windows\CurrentVersion\Run`
const registryValueName = "temporal-color-depth-switcher"

func addToStartup() error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}

	var hKey syscall.Handle
	subKeyPtr, _ := syscall.UTF16PtrFromString(registryRunPath)
	ret, _, err := procRegOpenKeyEx.Call(
		hkeyLocalMachine,
		uintptr(unsafe.Pointer(subKeyPtr)),
		0,
		keyWrite,
		uintptr(unsafe.Pointer(&hKey)),
	)
	if ret != 0 {
		return err
	}
	defer procRegCloseKey.Call(uintptr(hKey))

	valueNamePtr, _ := syscall.UTF16PtrFromString(registryValueName)
	valueData, _ := syscall.UTF16FromString(exePath)
	dataSize := uint32(len(valueData) * 2)

	ret, _, err = procRegSetValueEx.Call(
		uintptr(hKey),
		uintptr(unsafe.Pointer(valueNamePtr)),
		0,
		regSZ,
		uintptr(unsafe.Pointer(&valueData[0])),
		uintptr(dataSize),
	)
	if ret != 0 {
		return err
	}

	return nil
}

func removeFromStartup() error {
	var hKey syscall.Handle
	subKeyPtr, _ := syscall.UTF16PtrFromString(registryRunPath)
	ret, _, err := procRegOpenKeyEx.Call(
		hkeyLocalMachine,
		uintptr(unsafe.Pointer(subKeyPtr)),
		0,
		keyWrite,
		uintptr(unsafe.Pointer(&hKey)),
	)
	if ret != 0 {
		return err
	}
	defer procRegCloseKey.Call(uintptr(hKey))

	valueNamePtr, _ := syscall.UTF16PtrFromString(registryValueName)
	ret, _, err = procRegDeleteVal.Call(
		uintptr(hKey),
		uintptr(unsafe.Pointer(valueNamePtr)),
	)
	if ret != 0 {
		return err
	}

	return nil
}

func isStartupEnabled() bool {
	var hKey syscall.Handle
	subKeyPtr, _ := syscall.UTF16PtrFromString(registryRunPath)
	ret, _, _ := procRegOpenKeyEx.Call(
		hkeyLocalMachine,
		uintptr(unsafe.Pointer(subKeyPtr)),
		0,
		keyRead,
		uintptr(unsafe.Pointer(&hKey)),
	)
	if ret != 0 {
		return false
	}
	defer procRegCloseKey.Call(uintptr(hKey))

	valueNamePtr, _ := syscall.UTF16PtrFromString(registryValueName)
	ret, _, _ = procRegQueryVal.Call(
		uintptr(hKey),
		uintptr(unsafe.Pointer(valueNamePtr)),
		0,
		0,
		0,
		0,
	)
	return ret == 0
}
