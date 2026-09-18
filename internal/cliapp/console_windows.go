//go:build windows

package cliapp

import (
	"bufio"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetConsoleProcessList = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList")

// consoleExclusive 判断当前进程是否独占控制台。双击 exe / Win+R 启动时，系统为本进程
// 新建了一个控制台，挂在它上面的进程只有我们自己；从已有终端（cmd / PowerShell /
// Windows Terminal）启动时，控制台上至少还有 shell。GetConsoleProcessList 只返回 1 即独占。
func consoleExclusive() bool {
	var processes [2]uint32
	// 返回值是控制台上的进程数；缓冲区只装得下 2 个，返回 >2 也说明远不止我们一个进程
	count, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&processes[0])), uintptr(len(processes)))
	if count == 0 {
		return false
	}
	return count == 1
}

// pauseForError 在「独占控制台且命令出错」时暂停，等用户按回车再退出。
// 独占控制台（双击 exe / Win+R 启动）的窗口随进程退出即刻关闭，错误信息一闪而过——
// 这就是用户看到的「闪退」。从真实终端启动时控制台归 shell 所有，窗口不会消失，不做任何停顿；
// 脚本化调用（管道 / 重定向 stdin）时回车读取立即得到 EOF，同样不会卡住。
func pauseForError() {
	if !consoleExclusive() {
		return
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprint(os.Stderr, "按回车键退出…")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}
