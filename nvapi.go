//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

const (
	nvAPIFuncIDInitialize              = 0x0150E828
	nvAPIFuncIDUnload                  = 0xD22BDD7E
	nvAPIFuncIDEnumDisplayHandle       = 0x9ABDD40D
	nvAPIFuncIDGetDisplayName          = 0x22A78B05
	nvAPIFuncIDGetDisplayIDByName      = 0xAE457190
	nvAPIFuncIDDispColorControl        = 0x92F9D80D
	nvAPIFuncIDGetErrorMessage         = 0x6C2D048C
)

const (
	nvColorCmdGet        = 1
	nvColorCmdSet        = 2
)

const (
	nvBPCDefault = 0
	nvBPC6       = 1
	nvBPC8       = 2
	nvBPC10      = 3
	nvBPC12      = 4
	nvBPC16      = 5
)

const (
	nvDesktopColorDepthDefault       = 0
	nvDesktopColorDepth8BPC          = 1
	nvDesktopColorDepth10BPC         = 2
)

const (
	nvColorSelectionPolicyUser        = 0
	nvColorSelectionPolicyBestQuality = 1
)

const nvShortStringMax = 64

// NV_COLOR_DATA_V5 — 24 bytes with #pragma pack(push, 8)
// Verified from nvapi.h lines 7535-7549
type nvColorDataV5 struct {
	Version              uint32
	Size                 uint16
	Cmd                  uint8
	_                    uint8  // padding after cmd
	ColorFormat          uint8
	Colorimetry          uint8
	DynamicRange         uint8
	_                    uint8  // padding before bpc (align to 4)
	BPC                  uint32
	ColorSelectionPolicy uint32
	Depth                uint32
}

const nvColorDataV5Version = uint32(24 | (5 << 16)) // MAKE_NVAPI_VERSION(NV_COLOR_DATA_V5, 5)

type DisplayInfo struct {
	Index     int
	DisplayID uint32
	Name      string
}

type nvAPIState struct {
	dll               syscall.Handle
	queryInterface    uintptr
	initialize        uintptr
	unload            uintptr
	enumDisplayHandle uintptr
	getDisplayName    uintptr
	getDisplayIDByName uintptr
	dispColorControl  uintptr
	getErrorMessage   uintptr

	displays        []DisplayInfo
	isCurrently10Bit bool
}

var nvapi nvAPIState

func nvAPIQuery(functionID uint32) (uintptr, error) {
	ret, _, _ := syscall.SyscallN(nvapi.queryInterface, uintptr(functionID))
	if ret == 0 {
		return 0, fmt.Errorf("nvapi_QueryInterface failed for function ID 0x%08X", functionID)
	}
	return ret, nil
}

func nvAPIGetErrorMessage(status uintptr) string {
	if nvapi.getErrorMessage == 0 {
		return fmt.Sprintf("NvAPI error %d", status)
	}
	var buf [nvShortStringMax]byte
	syscall.SyscallN(nvapi.getErrorMessage, status, uintptr(unsafe.Pointer(&buf[0])))
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i])
		}
	}
	return string(buf[:])
}

func initNvAPI() error {
	dll, err := syscall.LoadLibrary("nvapi64.dll")
	if err != nil {
		return fmt.Errorf("failed to load nvapi64.dll: %w (is the NVIDIA driver installed?)", err)
	}
	nvapi.dll = dll

	qiAddr, err := syscall.GetProcAddress(dll, "nvapi_QueryInterface")
	if err != nil {
		return fmt.Errorf("failed to find nvapi_QueryInterface: %w", err)
	}
	nvapi.queryInterface = qiAddr

	funcIDs := []struct {
		id   uint32
		dest *uintptr
		name string
	}{
		{nvAPIFuncIDInitialize, &nvapi.initialize, "NvAPI_Initialize"},
		{nvAPIFuncIDUnload, &nvapi.unload, "NvAPI_Unload"},
		{nvAPIFuncIDEnumDisplayHandle, &nvapi.enumDisplayHandle, "NvAPI_EnumNvidiaDisplayHandle"},
		{nvAPIFuncIDGetDisplayName, &nvapi.getDisplayName, "NvAPI_GetAssociatedNvidiaDisplayName"},
		{nvAPIFuncIDGetDisplayIDByName, &nvapi.getDisplayIDByName, "NvAPI_DISP_GetDisplayIdByDisplayName"},
		{nvAPIFuncIDDispColorControl, &nvapi.dispColorControl, "NvAPI_Disp_ColorControl"},
		{nvAPIFuncIDGetErrorMessage, &nvapi.getErrorMessage, "NvAPI_GetErrorMessage"},
	}

	for _, f := range funcIDs {
		addr, err := nvAPIQuery(f.id)
		if err != nil {
			return fmt.Errorf("failed to resolve %s: %w", f.name, err)
		}
		*f.dest = addr
	}

	ret, _, _ := syscall.SyscallN(nvapi.initialize)
	if ret != 0 {
		return fmt.Errorf("NvAPI_Initialize failed: %s", nvAPIGetErrorMessage(ret))
	}

	return nil
}

func unloadNvAPI() {
	if nvapi.unload != 0 {
		syscall.SyscallN(nvapi.unload)
	}
	if nvapi.dll != 0 {
		syscall.FreeLibrary(nvapi.dll)
	}
}

func enumerateDisplays() ([]DisplayInfo, error) {
	var displays []DisplayInfo

	for i := uint32(0); ; i++ {
		var handle uintptr
		ret, _, _ := syscall.SyscallN(nvapi.enumDisplayHandle, uintptr(i), uintptr(unsafe.Pointer(&handle)))
		if ret != 0 {
			break
		}

		var nameBuf [nvShortStringMax]byte
		ret, _, _ = syscall.SyscallN(nvapi.getDisplayName, handle, uintptr(unsafe.Pointer(&nameBuf[0])))
		if ret != 0 {
			continue
		}

		name := ""
		for j, b := range nameBuf {
			if b == 0 {
				name = string(nameBuf[:j])
				break
			}
		}

		var displayID uint32
		namePtr, err := syscall.BytePtrFromString(name)
		if err != nil {
			continue
		}
		ret, _, _ = syscall.SyscallN(nvapi.getDisplayIDByName, uintptr(unsafe.Pointer(namePtr)), uintptr(unsafe.Pointer(&displayID)))
		if ret != 0 {
			continue
		}

		displays = append(displays, DisplayInfo{
			Index:     int(i),
			DisplayID: displayID,
			Name:      name,
		})
	}

	if len(displays) == 0 {
		return nil, fmt.Errorf("no NVIDIA displays found")
	}

	nvapi.displays = displays
	return displays, nil
}

func getColorData(displayID uint32) (*nvColorDataV5, error) {
	var colorData nvColorDataV5
	colorData.Version = nvColorDataV5Version
	colorData.Size = uint16(unsafe.Sizeof(colorData))
	colorData.Cmd = nvColorCmdGet

	ret, _, _ := syscall.SyscallN(nvapi.dispColorControl, uintptr(displayID), uintptr(unsafe.Pointer(&colorData)))
	if ret != 0 {
		return nil, fmt.Errorf("NvAPI_Disp_ColorControl GET failed: %s", nvAPIGetErrorMessage(ret))
	}
	return &colorData, nil
}

func setColorDepth(displayID uint32, bpc uint32, depth uint32) error {
	want10Bit := (bpc == nvBPC10)
	if want10Bit == nvapi.isCurrently10Bit {
		return nil
	}

	colorData, err := getColorData(displayID)
	if err != nil {
		return err
	}

	colorData.Version = nvColorDataV5Version
	colorData.Size = uint16(unsafe.Sizeof(*colorData))
	colorData.Cmd = nvColorCmdSet
	colorData.BPC = bpc
	colorData.Depth = depth
	colorData.ColorSelectionPolicy = nvColorSelectionPolicyUser

	ret, _, _ := syscall.SyscallN(nvapi.dispColorControl, uintptr(displayID), uintptr(unsafe.Pointer(colorData)))
	if ret != 0 {
		return fmt.Errorf("NvAPI_Disp_ColorControl SET failed: %s", nvAPIGetErrorMessage(ret))
	}

	nvapi.isCurrently10Bit = want10Bit
	return nil
}

func forceSetColorDepth(displayID uint32, bpc uint32, depth uint32) error {
	nvapi.isCurrently10Bit = !(bpc == nvBPC10)
	return setColorDepth(displayID, bpc, depth)
}
