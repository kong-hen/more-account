//go:build windows

package switcher

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

const (
	credTypeGeneric         = 1
	credPersistLocalMachine = 2
	winErrorNotFound        = syscall.Errno(1168)
	winCredTargetName       = "gemini:antigravity"
	winCredUserName         = "antigravity"
)

// winCredential 对应 Win32 ADVAPI32 CREDENTIALW 结构体布局。
type winCredential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        syscall.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

var (
	modAdvapi32   = syscall.NewLazyDLL("advapi32.dll")
	procCredWrite = modAdvapi32.NewProc("CredWriteW")
	procCredRead  = modAdvapi32.NewProc("CredReadW")
	procCredFree  = modAdvapi32.NewProc("CredFree")
)

func writeOSSystemCredential(payloadJSON string) error {
	targetPtr, err := syscall.UTF16PtrFromString(winCredTargetName)
	if err != nil {
		return fmt.Errorf("编码 Windows 凭据目标名失败: %w", err)
	}
	userPtr, err := syscall.UTF16PtrFromString(winCredUserName)
	if err != nil {
		return fmt.Errorf("编码 Windows 凭据用户名失败: %w", err)
	}

	blob := []byte(payloadJSON)
	var blobPtr *byte
	if len(blob) > 0 {
		blobPtr = &blob[0]
	}

	cred := winCredential{
		Type:               credTypeGeneric,
		TargetName:         targetPtr,
		CredentialBlobSize: uint32(len(blob)),
		CredentialBlob:     blobPtr,
		Persist:            credPersistLocalMachine,
		UserName:           userPtr,
	}

	r1, _, callErr := procCredWrite.Call(uintptr(unsafe.Pointer(&cred)), 0)
	// 显式保活传入 Win32 CredWriteW 的 Go 指针及缓冲区，避免 GC 在 syscall 期间回收或移动引用对象
	runtime.KeepAlive(targetPtr)
	runtime.KeepAlive(userPtr)
	runtime.KeepAlive(blob)
	runtime.KeepAlive(&cred)
	if r1 == 0 {
		return fmt.Errorf("写入 Windows 凭据管理器失败: %w", callErr)
	}
	return nil
}

func readOSSystemCredential() (string, error) {
	targetPtr, err := syscall.UTF16PtrFromString(winCredTargetName)
	if err != nil {
		return "", fmt.Errorf("编码 Windows 凭据目标名失败: %w", err)
	}

	var credPtr *winCredential
	r1, _, callErr := procCredRead.Call(
		uintptr(unsafe.Pointer(targetPtr)),
		uintptr(credTypeGeneric),
		0,
		uintptr(unsafe.Pointer(&credPtr)),
	)
	// 显式保活输入目标名与输出指针地址，满足 Go unsafe.Pointer 规则
	runtime.KeepAlive(targetPtr)
	runtime.KeepAlive(&credPtr)
	if r1 == 0 {
		if errors.Is(callErr, winErrorNotFound) {
			return "", nil
		}
		return "", fmt.Errorf("读取 Windows 凭据管理器失败: %w", callErr)
	}
	defer func() {
		if credPtr != nil {
			_, _, _ = procCredFree.Call(uintptr(unsafe.Pointer(credPtr)))
			runtime.KeepAlive(credPtr)
		}
	}()

	if credPtr == nil || credPtr.CredentialBlob == nil || credPtr.CredentialBlobSize == 0 {
		return "", nil
	}
	raw := unsafe.Slice(credPtr.CredentialBlob, int(credPtr.CredentialBlobSize))
	return decodeWindowsCredentialBlob(raw), nil
}

// decodeWindowsCredentialBlob 同时兼容 go-keyring 写入的 UTF-8 字节流与 Windows 原生工具写入的 UTF-16LE 字节流。
func decodeWindowsCredentialBlob(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	if len(raw)%2 == 0 && len(raw) >= 4 {
		hasUTF16BOM := raw[0] == 0xFF && raw[1] == 0xFE
		zeroOddCount := 0
		samplePairs := min(len(raw)/2, 16)
		for i := 0; i < samplePairs; i++ {
			if raw[i*2+1] == 0 {
				zeroOddCount++
			}
		}
		if hasUTF16BOM || zeroOddCount >= samplePairs*3/4 {
			start := 0
			if hasUTF16BOM {
				start = 2
			}
			u16 := make([]uint16, (len(raw)-start)/2)
			for i := range u16 {
				u16[i] = uint16(raw[start+i*2]) | (uint16(raw[start+i*2+1]) << 8)
			}
			return strings.TrimRight(string(utf16.Decode(u16)), "\x00")
		}
	}
	return strings.TrimRight(string(raw), "\x00")
}
