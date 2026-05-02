//go:build windows

package main

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

var (
	modKernel32              = syscall.NewLazyDLL("kernel32.dll")
	procCreateToolhelp32Snap = modKernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW      = modKernel32.NewProc("Process32FirstW")
	procProcess32NextW       = modKernel32.NewProc("Process32NextW")
	procCloseHandle          = modKernel32.NewProc("CloseHandle")
	procOpenProcess          = modKernel32.NewProc("OpenProcess")
	procWaitForSingleObject  = modKernel32.NewProc("WaitForSingleObject")
)

const (
	th32csSnapProcess = 0x00000002
	maxPath           = 260
	synchronize       = 0x00100000
	infinite          = 0xFFFFFFFF
)

type processEntry32W struct {
	Size            uint32
	CntUsage        uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	CntThreads      uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [maxPath]uint16
}

type processMonitor struct {
	onColorSwitch  func(enable10Bit bool)
	quit           chan struct{}
	activeGamePIDs map[uint32]string
	mu             sync.Mutex
}

func newProcessMonitor(onColorSwitch func(bool)) *processMonitor {
	return &processMonitor{
		onColorSwitch:  onColorSwitch,
		quit:           make(chan struct{}),
		activeGamePIDs: make(map[uint32]string),
	}
}

func (m *processMonitor) start() {
	go m.run()
}

func (m *processMonitor) stop() {
	close(m.quit)
}

func (m *processMonitor) run() {
	defer func() {
		if r := recover(); r != nil {
			logError("process monitor panic: %v", r)
		}
	}()

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		logError("COM init failed: %v", err)
		return
	}
	defer ole.CoUninitialize()

	unknown, err := oleutil.CreateObject("WbemScripting.SWbemLocator")
	if err != nil {
		logError("SWbemLocator creation failed: %v", err)
		return
	}
	defer unknown.Release()

	locator, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		logError("SWbemLocator QueryInterface failed: %v", err)
		return
	}
	defer locator.Release()

	serviceRaw, err := oleCallMethod(locator, "ConnectServer", ".", `ROOT\CIMV2`)
	if err != nil {
		logError("WMI ConnectServer failed: %v", err)
		return
	}
	service := serviceRaw.ToIDispatch()
	defer service.Release()

	startQuery := "SELECT * FROM Win32_ProcessStartTrace"
	startEnumRaw, err := oleCallMethod(service, "ExecNotificationQuery", startQuery)
	if err != nil {
		logError("WMI start event subscription failed: %v", err)
		return
	}
	startEnum := startEnumRaw.ToIDispatch()
	defer startEnum.Release()

	logInfo("WMI process monitor started")

	m.trackRunningExceptedProcesses()

	for {
		select {
		case <-m.quit:
			return
		default:
		}

		eventRaw, err := oleCallMethod(startEnum, "NextEvent", 1000)
		if err != nil {
			continue
		}
		event := eventRaw.ToIDispatch()
		if event == nil {
			continue
		}

		pidVariant, err := event.GetProperty("ProcessID")
		if err != nil {
			event.Release()
			continue
		}
		pid := uint32(pidVariant.Val)

		nameVariant, _ := event.GetProperty("ProcessName")
		eventName := ""
		if nameVariant != nil && nameVariant.Val != 0 {
			eventName = nameVariant.ToString()
		}
		event.Release()

		fullName := resolveProcessName(pid, eventName)
		if fullName != "" && isExceptedProcess(fullName) {
			m.trackAndWatch(pid, fullName)
		}
	}
}

func (m *processMonitor) trackAndWatch(pid uint32, processName string) {
	m.mu.Lock()
	if _, already := m.activeGamePIDs[pid]; already {
		m.mu.Unlock()
		return
	}
	m.activeGamePIDs[pid] = processName
	wasEmpty := len(m.activeGamePIDs) == 1
	m.mu.Unlock()

	if wasEmpty {
		logInfo("game started: %s (PID %d) -> switching to 10-bit", processName, pid)
		m.onColorSwitch(true)
	} else {
		logInfo("game started: %s (PID %d), already in 10-bit", processName, pid)
	}

	go m.waitForExit(pid, processName)
}

func (m *processMonitor) waitForExit(pid uint32, processName string) {
	handle, _, _ := procOpenProcess.Call(synchronize, 0, uintptr(pid))
	if handle == 0 {
		logError("failed to open process %s (PID %d) for wait", processName, pid)
		m.removeTrackedPID(pid, processName)
		return
	}
	defer procCloseHandle.Call(handle)

	procWaitForSingleObject.Call(handle, infinite)

	m.removeTrackedPID(pid, processName)
}

func (m *processMonitor) removeTrackedPID(pid uint32, processName string) {
	m.mu.Lock()
	delete(m.activeGamePIDs, pid)
	remaining := len(m.activeGamePIDs)
	m.mu.Unlock()

	if remaining == 0 {
		logInfo("game exited: %s (PID %d) -> switching to 8-bit", processName, pid)
		m.onColorSwitch(false)
	} else {
		logInfo("game exited: %s (PID %d), %d other game(s) still running", processName, pid, remaining)
	}
}

func (m *processMonitor) trackRunningExceptedProcesses() {
	snapshot, _, _ := procCreateToolhelp32Snap.Call(th32csSnapProcess, 0)
	if snapshot == uintptr(syscall.InvalidHandle) {
		return
	}
	defer procCloseHandle.Call(snapshot)

	var entry processEntry32W
	entry.Size = uint32(unsafe.Sizeof(entry))

	ret, _, _ := procProcess32FirstW.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	if ret == 0 {
		return
	}

	for {
		processName := syscall.UTF16ToString(entry.ExeFile[:])
		if isExceptedProcess(processName) {
			m.trackAndWatch(entry.ProcessID, processName)
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		ret, _, _ = procProcess32NextW.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
		if ret == 0 {
			break
		}
	}
}

func resolveProcessName(pid uint32, eventName string) string {
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(50 * time.Millisecond)
		}
		if name := getProcessNameByPID(pid); name != "" {
			return name
		}
	}
	if eventName != "" {
		logInfo("snapshot lookup failed for PID %d, using WMI event name: %s", pid, eventName)
		return eventName
	}
	return ""
}

func oleCallMethod(disp *ole.IDispatch, method string, args ...any) (*ole.VARIANT, error) {
	result, err := disp.CallMethod(method, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	return result, nil
}

func getProcessNameByPID(pid uint32) string {
	snapshot, _, _ := procCreateToolhelp32Snap.Call(th32csSnapProcess, 0)
	if snapshot == uintptr(syscall.InvalidHandle) {
		return ""
	}
	defer procCloseHandle.Call(snapshot)

	var entry processEntry32W
	entry.Size = uint32(unsafe.Sizeof(entry))

	ret, _, _ := procProcess32FirstW.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	if ret == 0 {
		return ""
	}

	for {
		if entry.ProcessID == pid {
			return syscall.UTF16ToString(entry.ExeFile[:])
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		ret, _, _ = procProcess32NextW.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
		if ret == 0 {
			break
		}
	}
	return ""
}

func getRunningExceptedProcesses() []string {
	snapshot, _, _ := procCreateToolhelp32Snap.Call(th32csSnapProcess, 0)
	if snapshot == uintptr(syscall.InvalidHandle) {
		return nil
	}
	defer procCloseHandle.Call(snapshot)

	var entry processEntry32W
	entry.Size = uint32(unsafe.Sizeof(entry))

	ret, _, _ := procProcess32FirstW.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	if ret == 0 {
		return nil
	}

	seen := make(map[string]struct{})
	var running []string

	for {
		processName := syscall.UTF16ToString(entry.ExeFile[:])
		lower := strings.ToLower(processName)
		if _, ok := seen[lower]; !ok {
			if isExceptedProcess(processName) {
				seen[lower] = struct{}{}
				running = append(running, processName)
			}
		}

		entry.Size = uint32(unsafe.Sizeof(entry))
		ret, _, _ = procProcess32NextW.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
		if ret == 0 {
			break
		}
	}

	return running
}
