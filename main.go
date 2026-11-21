package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:     "More Account",
		Width:     1320,
		Height:    840,
		MinWidth:  1024,
		MinHeight: 660,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		// 默认浅色画布背景，与默认浅色主题保持无缝一致
		BackgroundColour: &options.RGBA{R: 247, G: 248, B: 250, A: 255},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
		Mac: &mac.Options{
			TitleBar:             mac.TitleBarHiddenInset(),
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			About: &mac.AboutInfo{
				Title:   "More Account",
				Message: "轻量级 Antigravity 反重力账号管理工具",
			},
		},
	})

	if err != nil {
		log.Fatalf("启动 More Account 失败: %v", err)
	}
}
