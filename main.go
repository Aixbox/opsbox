package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"opsbox/internal/singleinstance"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// 单实例锁：已有实例存活时弹提示退出——SQLite 不会双开，端口稳定在首选值，
	// CLI 的端口发现因此可以依赖固定窗口（配合 server.json 与 /healthz 身份校验）。
	if !singleinstance.Acquire() {
		return
	}
	app := NewApp()
	err := wails.Run(&options.App{
		Title:     "opsbox",
		Width:     1280,
		Height:    820,
		MinWidth:  960,
		MinHeight: 640,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 255, G: 255, B: 255, A: 255},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind:             []interface{}{app},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}
