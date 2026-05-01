//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/getlantern/systray"
)

var (
	modUser32      = syscall.NewLazyDLL("user32.dll")
	modShell32     = syscall.NewLazyDLL("shell32.dll")
	procMessageBox = modUser32.NewProc("MessageBoxW")
	procIsAdmin    = modShell32.NewProc("IsUserAnAdmin")
	procCreateMutex = modKernel32.NewProc("CreateMutexW")
)

const (
	errorAlreadyExists = 183
	mbIconError        = 0x00000010
)

var (
	logger  *log.Logger
	monitor *processMonitor
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			setupLogging()
			logError("PANIC: %v", r)
			showErrorDialog(fmt.Sprintf("Fatal error: %v", r))
		}
	}()

	if !enforceSingleInstance() {
		return
	}

	if !isRunningAsAdmin() {
		showErrorDialog("This application requires administrator privileges.\nPlease run as administrator.")
		return
	}

	exePath, err := os.Executable()
	if err == nil {
		os.Chdir(filepath.Dir(exePath))
	}

	setupLogging()

	if err := loadConfig(); err != nil {
		fatal("Failed to load config: %v", err)
	}
	logInfo("config loaded")

	if err := initNvAPI(); err != nil {
		fatal("Failed to initialize NvAPI: %v", err)
	}
	logInfo("NvAPI initialized")

	displays, err := enumerateDisplays()
	if err != nil {
		fatal("Failed to enumerate displays: %v", err)
	}
	logInfo("found %d display(s)", len(displays))
	for _, d := range displays {
		logInfo("  display: %s (ID: %d)", d.Name, d.DisplayID)
	}

	if cfg.DisplayID == 0 || !isValidDisplayID(cfg.DisplayID) {
		cfg.DisplayIndex = 0
		cfg.DisplayID = displays[0].DisplayID
		cfg.DisplayName = displays[0].Name
		saveConfig()
	}

	rebuildProcessCache()

	if running := getRunningExceptedProcesses(); len(running) > 0 {
		logInfo("excepted process already running: %s -> switching to 10-bit", strings.Join(running, ", "))
		if err := setColorDepth(cfg.DisplayID, uint32(cfg.GameBPC), ); err != nil {
			logError("failed to switch to game color depth: %v", err)
		}
		updateStatusText(true, running[0])
	}

	monitor = newProcessMonitor(func(enable10Bit bool) {
		configMu.Lock()
		displayID := cfg.DisplayID
		defaultBPC := cfg.DefaultBPC
		gameBPC := cfg.GameBPC
		configMu.Unlock()

		if enable10Bit {
			if err := setColorDepth(displayID, uint32(gameBPC), ); err != nil {
				logError("failed to switch to 10-bit: %v", err)
			}
			running := getRunningExceptedProcesses()
			gameName := "game"
			if len(running) > 0 {
				gameName = running[0]
			}
			updateStatusText(true, gameName)
		} else {
			if err := setColorDepth(displayID, uint32(defaultBPC), ); err != nil {
				logError("failed to switch to 8-bit: %v", err)
			}
			updateStatusText(false, "")
		}
	})
	monitor.start()
	logInfo("process monitor started")

	logInfo("starting systray...")
	systray.Run(onTrayReady, func() {
		logInfo("shutting down...")

		if monitor != nil {
			monitor.stop()
		}

		configMu.Lock()
		displayID := cfg.DisplayID
		defaultBPC := cfg.DefaultBPC
		configMu.Unlock()

		if err := forceSetColorDepth(displayID, uint32(defaultBPC), ); err != nil {
			logError("failed to restore color depth on exit: %v", err)
		} else {
			logInfo("restored %d-bit color depth", bpcToHumanBits(defaultBPC))
		}

		unloadNvAPI()
		logInfo("exited cleanly")

		onTrayExit()
	})
	logInfo("systray.Run returned unexpectedly")
}

func enforceSingleInstance() bool {
	name, _ := syscall.UTF16PtrFromString("temporal-color-depth-switcher")
	_, _, err := procCreateMutex.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if err.(syscall.Errno) == errorAlreadyExists {
		return false
	}
	return true
}

func isRunningAsAdmin() bool {
	ret, _, _ := procIsAdmin.Call()
	return ret != 0
}

func setupLogging() {
	logFile, err := os.OpenFile("temporal-color-depth-switcher.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		logger = log.New(os.Stderr, "", log.LstdFlags)
		return
	}
	logger = log.New(logFile, "", log.LstdFlags)
}

func logInfo(format string, args ...any) {
	if logger != nil {
		logger.Printf("INFO: "+format, args...)
	}
}

func logError(format string, args ...any) {
	if logger != nil {
		logger.Printf("ERROR: "+format, args...)
	}
}

func fatal(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	logError("%s", msg)
	showErrorDialog(msg)
	os.Exit(1)
}

func showErrorDialog(message string) {
	msgPtr, _ := syscall.UTF16PtrFromString(message)
	titlePtr, _ := syscall.UTF16PtrFromString("Temporal Color Depth Switcher")
	procMessageBox.Call(0, uintptr(unsafe.Pointer(msgPtr)), uintptr(unsafe.Pointer(titlePtr)), mbIconError)
}

func isValidDisplayID(displayID uint32) bool {
	for _, d := range nvapi.displays {
		if d.DisplayID == displayID {
			return true
		}
	}
	return false
}

func bpcToHumanBits(bpc int) int {
	switch bpc {
	case nvBPC6:
		return 6
	case nvBPC8:
		return 8
	case nvBPC10:
		return 10
	case nvBPC12:
		return 12
	case nvBPC16:
		return 16
	default:
		return 0
	}
}
