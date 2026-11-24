//go:build darwin

package statusbar

import (
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"more-account/internal/statusbar/testbridge"
)

func TestMain(m *testing.M) {
	runtime.LockOSThread()
	codeCh := make(chan int, 1)
	go func() {
		codeCh <- m.Run()
		testbridge.StopAppLoop()
	}()
	// 在主线程运行完整 AppKit 事件循环，确保最小化动画、Apple Event 与主队列回调与真实运行态一致
	testbridge.RunAppLoop()
	os.Exit(<-codeCh)
}

func waitUntil(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("等待条件超时: %s", desc)
}

func TestDarwinWindowHooksAndDockReopen(t *testing.T) {
	testbridge.SetupCocoaFixture()
	defer testbridge.CleanupCocoaFixture()

	var showCount atomic.Int32
	ctrl := NewController(Callbacks{
		OnShowWindow: func() {
			showCount.Add(1)
		},
	})
	ctrl.Start(true)
	defer ctrl.Stop()

	// 打开可见状态栏图标，模拟真实运行态持有 NSStatusBarWindow
	ctrl.Update(Snapshot{
		Visible:     true,
		ButtonTitle: "80%",
		Tooltip:     "More Account",
	})

	waitUntil(t, 2*time.Second, "AppDelegate 应响应 applicationShouldHandleReopen:hasVisibleWindows:", testbridge.AppRespondsToReopen)
	if got := testbridge.FinishLaunchingCount(); got != 1 {
		t.Fatalf("刷新 delegate 缓存不应重复触发 applicationDidFinishLaunching:，期望 1 次，实际 %d 次", got)
	}

	// 1. 后台关闭拦截：触发 windowShouldClose: 应隐藏窗口且不转发给原退出逻辑
	testbridge.ResetOrigCloseCount()
	if allowed := testbridge.TriggerWindowShouldClose(); allowed {
		t.Fatal("开启后台关闭拦截时 windowShouldClose: 应返回 NO")
	}
	if testbridge.IsWindowVisible() {
		t.Fatal("触发 windowShouldClose: 后主窗口应被 orderOut 隐藏")
	}
	if got := testbridge.OrigCloseCount(); got != 0 {
		t.Fatalf("后台隐藏模式下不应调用原 windowShouldClose:，实际调用 %d 次", got)
	}

	// 2. 关键回归场景：即使 AppKit 传入 hasVisibleWindows=YES（状态栏图标/Cmd+H/最小化/辅助窗口等场景），
	// 点击 Dock 仍必须由原生钩子同步恢复主窗口并触发 OnShowWindow 回调
	showCount.Store(0)
	testbridge.TriggerAppReopen(true)
	waitUntil(t, 2*time.Second, "hasVisibleWindows=YES 时 Dock 重开应恢复主窗口并触发 OnShowWindow", func() bool {
		return showCount.Load() == 1 && testbridge.IsWindowVisible()
	})

	// 3. 连续多次关闭->重开循环边界验证
	for i := 0; i < 3; i++ {
		if allowed := testbridge.TriggerWindowShouldClose(); allowed {
			t.Fatalf("第 %d 次关闭拦截应返回 NO", i+1)
		}
		if testbridge.IsWindowVisible() {
			t.Fatalf("第 %d 次关闭后主窗口应不可见", i+1)
		}
		showCount.Store(0)
		testbridge.TriggerAppReopen(true)
		waitUntil(t, 2*time.Second, "连续关闭重开应每次都能恢复主窗口", func() bool {
			return showCount.Load() == 1 && testbridge.IsWindowVisible()
		})
	}

	// 4. 常规场景：hasVisibleWindows=NO 时点击 Dock（经由真实 kAEReopenApplication Apple Event）必须恢复主窗口并触发 OnShowWindow
	testbridge.HideWindowOrderOut()
	if testbridge.IsWindowVisible() {
		t.Fatal("测试前置隐藏窗口失败")
	}
	showCount.Store(0)
	if !testbridge.SendReopenAppleEvent() {
		t.Fatal("发送 kAEReopenApplication Apple Event 失败")
	}
	waitUntil(t, 2*time.Second, "真实 kAEReopenApplication 应恢复隐藏主窗口并触发 OnShowWindow", func() bool {
		return showCount.Load() == 1 && testbridge.IsWindowVisible()
	})

	// 5. Cmd+H 应用隐藏态恢复：hide: 后通过 Dock 重开与 ShowMainWindow() 均应解除隐藏并恢复窗口
	testbridge.HideApp()
	waitUntil(t, 2*time.Second, "等待应用进入 hide 隐藏态", testbridge.IsAppHidden)
	showCount.Store(0)
	testbridge.TriggerAppReopen(true)
	waitUntil(t, 2*time.Second, "Cmd+H 隐藏后点击 Dock 应解除隐藏并恢复主窗口", func() bool {
		return showCount.Load() == 1 && !testbridge.IsAppHidden() && testbridge.IsWindowVisible()
	})

	testbridge.HideApp()
	waitUntil(t, 2*time.Second, "等待应用再次进入 hide 隐藏态", testbridge.IsAppHidden)
	ctrl.ShowMainWindow()
	waitUntil(t, 2*time.Second, "ShowMainWindow() 应解除 Cmd+H 隐藏并恢复主窗口", func() bool {
		return !testbridge.IsAppHidden() && testbridge.IsWindowVisible()
	})

	// 6. 边界场景：关闭顶部状态栏图标（MacStatusBarMode=off）后，关闭到后台与 Dock 重开仍应正常工作
	ctrl.Update(Snapshot{
		Visible: false,
	})
	if allowed := testbridge.TriggerWindowShouldClose(); allowed {
		t.Fatal("关闭状态栏图标后，后台关闭拦截仍应生效")
	}
	if testbridge.IsWindowVisible() {
		t.Fatal("关闭状态栏图标后触发 windowShouldClose: 仍应隐藏主窗口")
	}
	showCount.Store(0)
	testbridge.TriggerAppReopen(true)
	waitUntil(t, 2*time.Second, "关闭状态栏图标后点击 Dock 仍应恢复主窗口", func() bool {
		return showCount.Load() == 1 && testbridge.IsWindowVisible()
	})

	// 7. 最小化恢复边界：窗口最小化后点击 Dock 必须取消最小化并恢复显示
	beforeDemini := testbridge.DeminiaturizeCount()
	testbridge.SetSimulatedMiniaturized(true)
	if !testbridge.IsWindowMiniaturized() {
		t.Fatal("测试前置设置最小化态失败")
	}
	showCount.Store(0)
	testbridge.TriggerAppReopen(true)
	waitUntil(t, 2*time.Second, "窗口最小化后点击 Dock 应取消最小化并恢复显示", func() bool {
		return showCount.Load() == 1 &&
			testbridge.DeminiaturizeCount() > beforeDemini &&
			!testbridge.IsWindowMiniaturized() &&
			testbridge.IsWindowVisible()
	})

	// 8. 主动调用 ShowMainWindow()（状态栏菜单「打开主窗口」/ 单实例唤起路径）也应恢复隐藏与最小化窗口
	testbridge.HideWindowOrderOut()
	if testbridge.IsWindowVisible() {
		t.Fatal("测试前置隐藏窗口失败")
	}
	ctrl.ShowMainWindow()
	waitUntil(t, 2*time.Second, "ShowMainWindow() 应恢复被 orderOut 隐藏的主窗口", testbridge.IsWindowVisible)

	beforeDemini = testbridge.DeminiaturizeCount()
	testbridge.SetSimulatedMiniaturized(true)
	ctrl.ShowMainWindow()
	waitUntil(t, 2*time.Second, "ShowMainWindow() 应恢复最小化主窗口", func() bool {
		return testbridge.DeminiaturizeCount() > beforeDemini &&
			!testbridge.IsWindowMiniaturized() &&
			testbridge.IsWindowVisible()
	})

	// 9. 关闭后台运行开关后，windowShouldClose: 应回落转发给原委托实现
	ctrl.SetCloseToBackground(false)
	testbridge.ResetOrigCloseCount()
	_ = testbridge.TriggerWindowShouldClose()
	if got := testbridge.OrigCloseCount(); got != 1 {
		t.Fatalf("关闭后台运行开关后应转发原 windowShouldClose:，实际调用 %d 次", got)
	}
}
