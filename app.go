package main

import "context"

// App 是 Wails 桌面应用的前端绑定入口。
type App struct {
	ctx context.Context
}

// NewApp 创建应用实例。
func NewApp() *App {
	return &App{}
}

// startup 在应用窗口初始化时调用。
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// shutdown 在应用关闭前释放资源。
func (a *App) shutdown(ctx context.Context) {
}

// Ping 用于检查前后端桥接连通性。
func (a *App) Ping() string {
	return "pong"
}
