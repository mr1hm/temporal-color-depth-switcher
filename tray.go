//go:build windows

package main

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"github.com/getlantern/systray"
)

var (
	modComdlg32         = syscall.NewLazyDLL("comdlg32.dll")
	procGetOpenFileName = modComdlg32.NewProc("GetOpenFileNameW")
)

const ofnFileMustExist = 0x00001000
const ofnPathMustExist = 0x00000800

type openFileName struct {
	StructSize      uint32
	Owner           uintptr
	Instance        uintptr
	Filter          *uint16
	CustomFilter    *uint16
	MaxCustomFilter uint32
	FilterIndex     uint32
	File            *uint16
	MaxFile         uint32
	FileTitle       *uint16
	MaxFileTitle    uint32
	InitialDir      *uint16
	Title           *uint16
	Flags           uint32
	FileOffset      uint16
	FileExtension   uint16
	DefExt          *uint16
	CustData        uintptr
	FnHook          uintptr
	TemplateName    *uint16
	PvReserved      uintptr
	DwReserved      uint32
	FlagsEx         uint32
}

var (
	statusItem      *systray.MenuItem
	displayItems    []*systray.MenuItem
	processItems    []*systray.MenuItem
	startupItem     *systray.MenuItem
)

func onTrayReady() {
	logInfo("onTrayReady called")
	systray.SetIcon(generateTrayIcon())
	logInfo("icon set")
	systray.SetTooltip("Temporal Color Depth Switcher")

	displays := nvapi.displays
	logInfo("building menu for %d displays", len(displays))

	displayMenu := systray.AddMenuItem("Display", "Select display")
	displayItems = make([]*systray.MenuItem, len(displays))
	for i, d := range displays {
		label := d.Name
		if d.MonitorName != "" {
			label = fmt.Sprintf("%s (%s)", d.Name, d.MonitorName)
		}
		displayItems[i] = displayMenu.AddSubMenuItem(label, fmt.Sprintf("Switch on %s", label))
		if d.DisplayID == cfg.DisplayID {
			displayItems[i].Check()
		}
		go handleDisplaySelect(i, d)
	}

	systray.AddSeparator()

	statusItem = systray.AddMenuItem("Status: 8-bit (idle)", "Current color depth status")
	statusItem.Disable()

	systray.AddSeparator()

	processMenu := systray.AddMenuItem("Game Processes", "Manage game process list")
	rebuildProcessMenu(processMenu)

	addProcessItem := processMenu.AddSubMenuItem("Add process...", "Add a game executable")
	go func() {
		for {
			<-addProcessItem.ClickedCh
			filePath := openFileDialog()
			if filePath == "" {
				continue
			}
			processName := extractFileName(filePath)
			if err := addProcessException(processName); err != nil {
				logError("failed to add process: %v", err)
				continue
			}
			logInfo("added process exception: %s", processName)
			rebuildProcessMenu(processMenu)
		}
	}()

	systray.AddSeparator()

	startupItem = systray.AddMenuItem("Start with Windows", "Run on Windows startup")
	if isStartupEnabled() {
		startupItem.Check()
	}
	go func() {
		for {
			<-startupItem.ClickedCh
			if startupItem.Checked() {
				if err := removeFromStartup(); err != nil {
					logError("failed to remove from startup: %v", err)
					continue
				}
				startupItem.Uncheck()
			} else {
				if err := addToStartup(); err != nil {
					logError("failed to add to startup: %v", err)
					continue
				}
				startupItem.Check()
			}
		}
	}()

	systray.AddSeparator()

	exitItem := systray.AddMenuItem("Exit", "Exit the application")
	go func() {
		<-exitItem.ClickedCh
		systray.Quit()
	}()
}

func onTrayExit() {
}

func handleDisplaySelect(index int, display DisplayInfo) {
	for {
		<-displayItems[index].ClickedCh
		for _, item := range displayItems {
			item.Uncheck()
		}
		displayItems[index].Check()

		if err := setDisplay(index, display.DisplayID, display.Name); err != nil {
			logError("failed to save display selection: %v", err)
		}

		colorData, err := getColorData(display.DisplayID)
		if err != nil {
			logError("failed to read color data for new display: %v", err)
			nvapi.isCurrently10Bit = false
		} else {
			nvapi.isCurrently10Bit = (colorData.BPC == nvBPC10)
		}
		logInfo("display changed to %s (ID: %d), currently 10-bit: %v", display.Name, display.DisplayID, nvapi.isCurrently10Bit)
	}
}

func rebuildProcessMenu(parent *systray.MenuItem) {
	for _, item := range processItems {
		item.Hide()
	}
	processItems = nil

	configMu.Lock()
	processes := make([]string, len(cfg.ProcessExceptions))
	copy(processes, cfg.ProcessExceptions)
	configMu.Unlock()

	for _, name := range processes {
		item := parent.AddSubMenuItem(name, fmt.Sprintf("Click to remove %s", name))
		processItems = append(processItems, item)
		go handleProcessRemove(name, item, parent)
	}
}

func handleProcessRemove(processName string, item *systray.MenuItem, parent *systray.MenuItem) {
	<-item.ClickedCh

	confirmed := showConfirmDialog(fmt.Sprintf("Remove '%s' from game process list?", processName))
	if !confirmed {
		return
	}

	if err := removeProcessException(processName); err != nil {
		logError("failed to remove process: %v", err)
		return
	}

	logInfo("removed process exception: %s", processName)
	rebuildProcessMenu(parent)
}

func updateStatusText(is10Bit bool, gameName string) {
	if statusItem == nil {
		return
	}
	if is10Bit {
		statusItem.SetTitle(fmt.Sprintf("Status: 10-bit (%s)", gameName))
	} else {
		statusItem.SetTitle("Status: 8-bit (idle)")
	}
}

func buildDoubleNullFilter(parts []string) []uint16 {
	var result []uint16
	for _, part := range parts {
		encoded, _ := syscall.UTF16FromString(part)
		result = append(result, encoded...)
	}
	result = append(result, 0)
	return result
}

func openFileDialog() string {
	var fileBuf [maxPath]uint16

	filter := buildDoubleNullFilter([]string{
		"Executables (*.exe)", "*.exe",
		"All Files (*.*)", "*.*",
	})
	title, _ := syscall.UTF16FromString("Select Game Executable")

	ofn := openFileName{
		StructSize: uint32(unsafe.Sizeof(openFileName{})),
		Filter:     &filter[0],
		File:       &fileBuf[0],
		MaxFile:    maxPath,
		Title:      &title[0],
		Flags:      ofnFileMustExist | ofnPathMustExist,
	}

	ret, _, _ := procGetOpenFileName.Call(uintptr(unsafe.Pointer(&ofn)))
	if ret == 0 {
		return ""
	}

	return syscall.UTF16ToString(fileBuf[:])
}

func extractFileName(filePath string) string {
	parts := strings.Split(filePath, "\\")
	if len(parts) == 0 {
		return filePath
	}
	name := parts[len(parts)-1]
	if name == "" {
		return filePath
	}
	return name
}

func showConfirmDialog(message string) bool {
	msgPtr, _ := syscall.UTF16PtrFromString(message)
	titlePtr, _ := syscall.UTF16PtrFromString("Temporal Color Depth Switcher")

	modUser32 := syscall.NewLazyDLL("user32.dll")
	procMessageBox := modUser32.NewProc("MessageBoxW")

	const mbYesNo = 0x00000004
	const mbIconQuestion = 0x00000020
	const idYes = 6

	ret, _, _ := procMessageBox.Call(0, uintptr(unsafe.Pointer(msgPtr)), uintptr(unsafe.Pointer(titlePtr)), mbYesNo|mbIconQuestion)
	return ret == idYes
}
