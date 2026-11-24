//go:build darwin

package testbridge

/*
#cgo darwin CFLAGS: -x objective-c -fobjc-arc -Wall
#cgo darwin LDFLAGS: -framework Cocoa
#include "bridge_darwin.h"
*/
import "C"

// RunAppLoop 在主线程启动真实 Cocoa [NSApp run] 主事件循环。
func RunAppLoop() {
	C.MATestRunAppLoop()
}

// StopAppLoop 通知主线程退出 Cocoa 主事件循环。
func StopAppLoop() {
	C.MATestStopAppLoop()
}

// SetupCocoaFixture 初始化单元测试使用的 AppDelegate、WindowDelegate 与透明离屏测试窗口。
func SetupCocoaFixture() {
	C.MATestSetupCocoaFixture()
}

// CleanupCocoaFixture 在测试结束后隐藏测试窗口。
func CleanupCocoaFixture() {
	C.MATestCleanupCocoaFixture()
}

// AppRespondsToReopen 判断当前 NSApp.delegate 是否响应 applicationShouldHandleReopen:hasVisibleWindows:。
func AppRespondsToReopen() bool {
	return bool(C.MATestAppRespondsToReopen())
}

// TriggerWindowShouldClose 直接在主线程调用测试窗口委托的 windowShouldClose:。
func TriggerWindowShouldClose() bool {
	return bool(C.MATestTriggerWindowShouldClose())
}

// TriggerAppReopen 直接在主线程调用当前 NSApp.delegate 的 applicationShouldHandleReopen:hasVisibleWindows:。
func TriggerAppReopen(hasVisibleWindows bool) bool {
	return bool(C.MATestTriggerAppReopen(C.bool(hasVisibleWindows)))
}

// SendReopenAppleEvent 向当前进程发送真实 kAEReopenApplication Apple Event（模拟点击 Dock 图标）。
func SendReopenAppleEvent() bool {
	return bool(C.MATestSendReopenAppleEvent())
}

// HideWindowOrderOut 通过 orderOut:nil 隐藏测试窗口。
func HideWindowOrderOut() {
	C.MATestHideWindowOrderOut()
}

// HideApp 模拟 Cmd+H 调用 [NSApp hide:nil]。
func HideApp() {
	C.MATestHideApp()
}

// SetSimulatedMiniaturized 设置测试窗口的最小化状态（用于无 WindowServer 动画竞态地确定性验证 deminiaturize: 分支）。
func SetSimulatedMiniaturized(miniaturized bool) {
	C.MATestSetSimulatedMiniaturized(C.bool(miniaturized))
}

// IsWindowVisible 返回当前测试窗口是否处于可见状态。
func IsWindowVisible() bool {
	return bool(C.MATestIsWindowVisible())
}

// IsWindowMiniaturized 返回当前测试窗口是否处于最小化状态。
func IsWindowMiniaturized() bool {
	return bool(C.MATestIsWindowMiniaturized())
}

// IsAppHidden 返回当前 NSApp 是否处于 hide 隐藏态。
func IsAppHidden() bool {
	return bool(C.MATestIsAppHidden())
}

// OrigCloseCount 返回原始 windowShouldClose: 被透传调用的次数。
func OrigCloseCount() int {
	return int(C.MATestOrigCloseCount())
}

// ResetOrigCloseCount 重置原始 windowShouldClose: 调用计数。
func ResetOrigCloseCount() {
	C.MATestResetOrigCloseCount()
}

// FinishLaunchingCount 返回测试 AppDelegate 收到 applicationDidFinishLaunching: 的次数。
func FinishLaunchingCount() int {
	return int(C.MATestFinishLaunchingCount())
}

// DeminiaturizeCount 返回测试窗口收到 deminiaturize: 调用的次数。
func DeminiaturizeCount() int {
	return int(C.MATestDeminiaturizeCount())
}
