package cliapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// Run 执行根命令并把 ExitError 映射为进程退出码；其他错误退出码 4。
// 出错（含 panic）时若当前进程独占控制台（双击 exe / Win+R 启动），先等用户按回车再退出，
// 否则窗口随进程立刻关闭，错误信息一闪而过——这就是用户看到的「闪退」，见 pauseForError。
func Run(root *cobra.Command) {
	root.SilenceUsage = true
	root.SilenceErrors = true
	code := runRoot(root)
	if code != 0 {
		pauseForError()
	}
	os.Exit(code)
}

func runRoot(root *cobra.Command) (code int) {
	defer func() {
		// panic 不让它直接崩：打印堆栈供排查，走统一的错误出口（独占控制台时同样暂停）
		if recovered := recover(); recovered != nil {
			fmt.Fprintln(os.Stderr, "程序异常崩溃:", recovered)
			fmt.Fprintln(os.Stderr, string(debug.Stack()))
			code = ExitAPI
		}
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	var exitErr *ExitError
	code = ExitAPI
	if errors.As(err, &exitErr) {
		code = exitErr.Code
	}
	if msg := err.Error(); msg != "" {
		fmt.Fprintln(os.Stderr, "错误: "+msg)
	}
	return code
}
