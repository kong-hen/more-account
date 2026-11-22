package main

import (
	"embed"
	"log"
	"runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var appIcon []byte

func buildApplicationMenu(app *App) *menu.Menu {
	// 仅在 macOS 配置系统顶部原生菜单栏；Windows 保持 nil 避免窗口顶部出现冗余菜单条。
	if runtime.GOOS != "darwin" {
		return nil
	}
	appMenu := menu.NewMenu()
	appMenu.Append(menu.AppMenu())

	fileMenu := appMenu.AddSubmenu("文件")
	fileMenu.AddText("关闭窗口", keys.CmdOrCtrl("w"), func(_ *menu.CallbackData) {
		app.HandleCloseWindowShortcut()
	})
	fileMenu.AddSeparator()
	fileMenu.AddText("显示主窗口", nil, func(_ *menu.CallbackData) {
		app.ShowMainWindow()
	})

	appMenu.Append(menu.EditMenu())
	appMenu.Append(menu.WindowMenu())
	return appMenu
}

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:     "More Account",
		Width:     1160,
		Height:    800,
		MinWidth:  980,
		MinHeight: 640,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		Menu: buildApplicationMenu(app),
		// 默认浅色画布背景，与默认浅色主题保持无缝一致
		BackgroundColour: &options.RGBA{R: 247, G: 248, B: 250, A: 255},
		OnStartup:        app.startup,
		OnBeforeClose:    app.beforeClose,
		OnShutdown:       app.shutdown,
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "com.more-account.desktop.singleton",
			OnSecondInstanceLaunch: func(_ options.SecondInstanceData) {
				app.ShowMainWindow()
			},
		},
		Bind: []interface{}{
			app,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			DisableWindowIcon:    false,
		},
		Mac: &mac.Options{
			TitleBar:             mac.TitleBarHiddenInset(),
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			About: &mac.AboutInfo{
				Title:   "More Account",
				Message: "轻量级 Antigravity 反重力账号管理工具",
				Icon:    appIcon,
			},
		},
	})

	if err != nil {
		log.Fatalf("启动 More Account 失败: %v", err)
	}
}
