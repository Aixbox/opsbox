# CLI 嵌入目录（构建产物，不进 git）

`scripts/build-all.ps1` 会先把 sshctl / sqlctl / redisctl 构建到本目录并写入 VERSION，
然后执行 `wails build`——主程序通过 go:embed 携带它们，实现应用内一键安装 CLI。

本目录仅 README.md 提交到 git；`*.exe` 与 `VERSION` 见 .gitignore。
方案讨论中：若最终不采用「应用内安装」路线，本目录与 climgr.go 将一并移除。
