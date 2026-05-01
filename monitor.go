//go:build windows

package main

import (
	"fmt"
	"runtime"
	"strings"
	"syscall"
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
)

const (
	th32csSnapProcess = 0x00000002
	maxPath           = 260
)

type processEntry32W struct {
	Size              uint32
	CntUsage          uint32
	ProcessID         uint32
	DefaultHeapID     uintptr
	ModuleID          uint32
	CntThreads        uint32
	ParentProcessID   uint32
	PriClassBase      int32
	Flags             uint32
	ExeFile           [maxPath]uint16
}

type processMonitor struct {
	onColorSwitch func(enable10Bit bool)
	quit          chan struct{}
}

func newProcessMonitor(onColorSwitch func(bool)) *processMonitor {
	return &processMonitor{
		onColorSwitch: onColorSwitch,
		quit:          make(chan struct{}),
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

	stopQuery := "SELECT * FROM Win32_ProcessStopTrace"
	stopEnumRaw, err := oleCallMethod(service, "ExecNotificationQuery", stopQuery)
	if err != nil {
		logError("WMI stop event subscription failed: %v", err)
		return
	}
	stopEnum := stopEnumRaw.ToIDispatch()
	defer stopEnum.Release()

	logInfo("WMI process monitor started")

	startEvents := make(chan string, 8)
	stopEvents := make(chan string, 8)

	go m.pollWMIEvents(startEnum, startEvents)
	go m.pollWMIEvents(stopEnum, stopEvents)

	for {
		select {
		case <-m.quit:
			return
		case processName := <-startEvents:
			if isExceptedProcess(processName) {
				logInfo("game started: %s -> switching to 10-bit", processName)
				m.onColorSwitch(true)
			}
		case processName := <-stopEvents:
			if isExceptedProcess(processName) {
				if !anyExceptedProcessRunning() {
					logInfo("game stopped: %s -> switching to 8-bit", processName)
					m.onColorSwitch(false)
				} else {
					logInfo("game stopped: %s, but other excepted process still running", processName)
				}
			}
		}
	}
}

func (m *processMonitor) pollWMIEvents(enumerator *ole.IDispatch, out chan<- string) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		logError("COM init failed in event poller: %v", err)
		return
	}
	defer ole.CoUninitialize()

	for {
		select {
		case <-m.quit:
			return
		default:
		}

		eventRaw, err := oleCallMethod(enumerator, "NextEvent", 1000)
		if err != nil {
			continue
		}
		event := eventRaw.ToIDispatch()
		if event == nil {
			continue
		}

		nameVariant, err := event.GetProperty("ProcessName")
		if err != nil {
			event.Release()
			continue
		}

		processName := nameVariant.ToString()
		event.Release()

		if processName != "" {
			out <- processName
		}
	}
}

func oleCallMethod(disp *ole.IDispatch, method string, args ...any) (*ole.VARIANT, error) {
	result, err := disp.CallMethod(method, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	return result, nil
}

func anyExceptedProcessRunning() bool {
	snapshot, _, _ := procCreateToolhelp32Snap.Call(th32csSnapProcess, 0)
	if snapshot == uintptr(syscall.InvalidHandle) {
		return false
	}
	defer procCloseHandle.Call(snapshot)

	var entry processEntry32W
	entry.Size = uint32(unsafe.Sizeof(entry))

	ret, _, _ := procProcess32FirstW.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	if ret == 0 {
		return false
	}

	for {
		processName := syscall.UTF16ToString(entry.ExeFile[:])
		if isExceptedProcess(processName) {
			return true
		}

		entry.Size = uint32(unsafe.Sizeof(entry))
		ret, _, _ = procProcess32NextW.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
		if ret == 0 {
			break
		}
	}

	return false
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
