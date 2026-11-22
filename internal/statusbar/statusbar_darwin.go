//go:build darwin

package statusbar

/*
#cgo darwin CFLAGS: -x objective-c -fobjc-arc
#cgo darwin LDFLAGS: -framework Cocoa
#include <stdlib.h>
#include "statusbar_darwin.h"
*/
import "C"

import (
	_ "embed"
	"encoding/json"
	"sync"
	"unsafe"
)

//go:embed assets/tray_icon_template.png
var trayIconTemplatePNG []byte

var (
	activeMu sync.RWMutex
	activeCB Callbacks
)

type darwinController struct {
	mu      sync.Mutex
	started bool
	cb      Callbacks
}

// NewController 在 macOS 下创建基于原生 AppKit 的状态栏与窗口钩子控制器。
func NewController(cb Callbacks) Controller {
	return &darwinController{cb: cb}
}

func (c *darwinController) Start(closeToBackground bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	activeMu.Lock()
	activeCB = c.cb
	activeMu.Unlock()

	var iconPtr unsafe.Pointer
	iconLen := C.int(len(trayIconTemplatePNG))
	if len(trayIconTemplatePNG) > 0 {
		iconPtr = unsafe.Pointer(&trayIconTemplatePNG[0])
	}
	C.MAStatusBarStart(iconPtr, iconLen, C.bool(closeToBackground))
	c.started = true
}

func (c *darwinController) SetCloseToBackground(enabled bool) {
	C.MAStatusBarSetCloseToBackground(C.bool(enabled))
}

func (c *darwinController) Update(snap Snapshot) {
	c.mu.Lock()
	started := c.started
	c.mu.Unlock()
	if !started {
		return
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return
	}
	cs := C.CString(string(raw))
	defer C.free(unsafe.Pointer(cs))
	C.MAStatusBarUpdateJSON(cs)
}

func (c *darwinController) Stop() {
	c.mu.Lock()
	c.started = false
	c.mu.Unlock()

	activeMu.Lock()
	activeCB = Callbacks{}
	activeMu.Unlock()

	C.MAStatusBarStop()
}

func getCallbacks() Callbacks {
	activeMu.RLock()
	defer activeMu.RUnlock()
	return activeCB
}

//export maStatusBarFetchSnapshotJSON
func maStatusBarFetchSnapshotJSON() *C.char {
	cb := getCallbacks()
	if cb.BuildSnapshot == nil {
		return nil
	}
	snap := cb.BuildSnapshot()
	raw, err := json.Marshal(snap)
	if err != nil {
		return nil
	}
	return C.CString(string(raw))
}

//export maStatusBarOnShowWindow
func maStatusBarOnShowWindow() {
	cb := getCallbacks()
	if cb.OnShowWindow != nil {
		go cb.OnShowWindow()
	}
}

//export maStatusBarOnSwitchAccount
func maStatusBarOnSwitchAccount(accountID *C.char) {
	id := C.GoString(accountID)
	cb := getCallbacks()
	if cb.OnSwitchAccount != nil && id != "" {
		go cb.OnSwitchAccount(id)
	}
}

//export maStatusBarOnRefreshCurrent
func maStatusBarOnRefreshCurrent() {
	cb := getCallbacks()
	if cb.OnRefreshCurrent != nil {
		go cb.OnRefreshCurrent()
	}
}

//export maStatusBarOnRefreshAll
func maStatusBarOnRefreshAll() {
	cb := getCallbacks()
	if cb.OnRefreshAll != nil {
		go cb.OnRefreshAll()
	}
}

//export maStatusBarOnTogglePrivacy
func maStatusBarOnTogglePrivacy() {
	cb := getCallbacks()
	if cb.OnTogglePrivacy != nil {
		go cb.OnTogglePrivacy()
	}
}

//export maStatusBarOnQuit
func maStatusBarOnQuit() {
	cb := getCallbacks()
	if cb.OnQuit != nil {
		go cb.OnQuit()
	}
}
