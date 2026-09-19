package sshx

import (
	"path"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Verdict 是命令的静态分析结果。
//
//   - Blocked：命中内置高危规则或自定义黑名单，任何模式下都拒绝执行；
//   - Dynamic：命令包含无法静态判定的部分（eval / sh -c / 管道进 shell / 目标来自变量等），
//     不拦截，但 audit 模式下也应升级为人工审批。
type Verdict struct {
	Blocked bool   `json:"blocked"`
	Rule    string `json:"rule,omitempty"`
	Reason  string `json:"reason,omitempty"`

	Dynamic       bool   `json:"dynamic"`
	DynamicReason string `json:"dynamicReason,omitempty"`
}

// Analyze 对一条 shell 命令做三层检查：自定义正则黑名单 → 基于 mvdan/sh 语法树的内置高危动作 → 动态命令识别。
// custom 可为 nil。解析失败（非法 shell 语法）按 Dynamic 处理，交给人判断。
func Analyze(command string, custom *Blacklist) Verdict {
	verdict := Verdict{}
	if rule, hit := custom.Match(command); hit {
		verdict.Blocked, verdict.Rule, verdict.Reason = true, rule, "命中自定义黑名单规则"
		return verdict
	}
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		verdict.Dynamic, verdict.DynamicReason = true, "命令无法解析为 shell 语法，需人工确认"
		return verdict
	}
	analyzer := &analyzer{verdict: &verdict}
	syntax.Walk(file, analyzer.visit)
	return verdict
}

type analyzer struct {
	verdict *Verdict
}

func (a *analyzer) block(rule, reason string) {
	if !a.verdict.Blocked {
		a.verdict.Blocked, a.verdict.Rule, a.verdict.Reason = true, rule, reason
	}
}

func (a *analyzer) dynamic(reason string) {
	if !a.verdict.Dynamic {
		a.verdict.Dynamic, a.verdict.DynamicReason = true, reason
	}
}

func (a *analyzer) visit(node syntax.Node) bool {
	switch n := node.(type) {
	case *syntax.Stmt:
		for _, redirect := range n.Redirs {
			a.checkRedirect(redirect)
		}
	case *syntax.CallExpr:
		a.checkCall(n, false)
	case *syntax.BinaryCmd:
		if n.Op == syntax.Pipe || n.Op == syntax.PipeAll {
			a.checkPipeline(n)
		}
	case *syntax.FuncDecl:
		a.checkFuncDecl(n)
	}
	return true
}

// ---- 词与命令解析 ----

// word 是一个 shell 词的静态视图：text 是尽力拼出的字面值（变量展开用占位符），dynamic 表示含展开。
type word struct {
	text    string
	dynamic bool
	// procSubst 表示 <(...) 形式（bash <(curl ...) 是典型的远程脚本执行）。
	procSubst bool
}

func wordOf(w *syntax.Word) word {
	if w == nil {
		return word{}
	}
	var result word
	var builder strings.Builder
	var walk func(parts []syntax.WordPart)
	walk = func(parts []syntax.WordPart) {
		for _, part := range parts {
			switch p := part.(type) {
			case *syntax.Lit:
				builder.WriteString(p.Value)
			case *syntax.SglQuoted:
				builder.WriteString(p.Value)
			case *syntax.DblQuoted:
				walk(p.Parts)
			case *syntax.ParamExp:
				if p.Param != nil && p.Param.Value == "HOME" && !p.Excl && !p.Length && p.Index == nil && p.Slice == nil && p.Repl == nil && p.Exp == nil {
					// $HOME / ${HOME} 与 ~ 等价，保留给 rootish 判断
					builder.WriteString("~")
					continue
				}
				result.dynamic = true
				builder.WriteString("${...}")
			case *syntax.CmdSubst:
				result.dynamic = true
				builder.WriteString("$(...)")
			case *syntax.ArithmExp:
				result.dynamic = true
				builder.WriteString("$((...))")
			case *syntax.ProcSubst:
				result.dynamic, result.procSubst = true, true
				builder.WriteString("<(...)")
			case *syntax.ExtGlob:
				builder.WriteString("*")
			case *syntax.BraceExp:
				builder.WriteString("{...}")
			default:
				result.dynamic = true
			}
		}
	}
	walk(w.Parts)
	result.text = builder.String()
	return result
}

// resolved 是剥掉 sudo/nohup/env/xargs 等包装后的「真正要执行的命令」。
type resolved struct {
	name string
	args []word
	// viaXargs 表示命令的目标来自上游输出（xargs），静态无法确定。
	viaXargs bool
	// nameDynamic 表示命令名本身来自变量/命令替换。
	nameDynamic bool
}

func commandName(w word) string {
	name := strings.TrimPrefix(w.text, `\`)
	if strings.Contains(name, "/") {
		name = path.Base(name)
	}
	return name
}

// wrapperOptionArity 列出各包装命令里「后面跟一个独立参数」的选项。
var wrapperOptionArity = map[string]map[string]bool{
	"sudo":    {"-u": true, "-g": true, "-U": true, "-h": true, "-p": true, "-C": true, "-D": true, "-r": true, "-t": true, "-T": true, "-R": true},
	"doas":    {"-u": true, "-C": true},
	"nice":    {"-n": true},
	"ionice":  {"-c": true, "-n": true, "-p": true},
	"stdbuf":  {"-i": true, "-o": true, "-e": true},
	"env":     {"-u": true, "-C": true, "-S": true},
	"xargs":   {"-n": true, "-I": true, "-i": true, "-L": true, "-l": true, "-P": true, "-d": true, "-a": true, "-s": true, "-E": true, "-e": true},
	"timeout": {"-s": true, "-k": true},
	"chroot":  {"--userspec": true, "--groups": true},
	"setsid":  {},
	"nohup":   {},
	"time":    {"-f": true, "-o": true},
	"command": {},
	"builtin": {},
	"exec":    {"-a": true},
	"busybox": {},
	"flock":   {"-w": true, "-E": true},
	"strace":  {"-o": true, "-e": true, "-p": true},
}

// wrapperPositional 是包装命令在选项之后、真正命令之前还要吃掉的位置参数个数。
var wrapperPositional = map[string]int{"timeout": 1, "chroot": 1, "flock": 1}

func resolve(call *syntax.CallExpr) resolved {
	words := make([]word, 0, len(call.Args))
	for _, arg := range call.Args {
		words = append(words, wordOf(arg))
	}
	result := resolved{}
	for len(words) > 0 {
		head := words[0]
		if head.dynamic {
			result.nameDynamic = true
			result.name = head.text
			result.args = words[1:]
			return result
		}
		name := commandName(head)
		arity, isWrapper := wrapperOptionArity[name]
		if !isWrapper {
			result.name = name
			result.args = words[1:]
			return result
		}
		if name == "xargs" {
			result.viaXargs = true
		}
		rest := words[1:]
		// 跳过选项（含带参选项）、env 的 VAR=val、`--` 终止符
		for len(rest) > 0 {
			token := rest[0].text
			switch {
			case token == "--":
				rest = rest[1:]
				goto positional
			case strings.HasPrefix(token, "-") && len(token) > 1:
				if arity[token] && len(rest) > 1 {
					rest = rest[2:]
				} else {
					rest = rest[1:]
				}
			case name == "env" && strings.Contains(token, "=") && !strings.HasPrefix(token, "="):
				rest = rest[1:]
			default:
				goto positional
			}
		}
	positional:
		if skip := wrapperPositional[name]; skip > 0 && len(rest) >= skip {
			rest = rest[skip:]
		}
		if len(rest) == 0 {
			// 只有包装命令本身（如 `sudo -i`、`nohup`），没有可分析的内层命令
			result.name = name
			return result
		}
		words = rest
	}
	return result
}

// ---- 规则 ----

var blockDevicePattern = regexp.MustCompile(`^/dev/(sd[a-z]|hd[a-z]|vd[a-z]|xvd[a-z]|nvme\d|mmcblk\d|md\d|dm-\d|mapper/|disk/|loop\d|sr\d|zd\d|nbd\d|rbd\d)`)

var (
	rootishDirs = map[string]bool{
		"/": true, "~": true,
		"/bin": true, "/boot": true, "/dev": true, "/etc": true, "/home": true, "/lib": true, "/lib32": true, "/lib64": true,
		"/proc": true, "/root": true, "/run": true, "/sbin": true, "/sys": true, "/usr": true, "/var": true, "/srv": true, "/opt": true,
	}
	shellNames        = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "mksh": true, "ash": true, "fish": true, "csh": true, "tcsh": true}
	powerCommands     = map[string]bool{"shutdown": true, "reboot": true, "halt": true, "poweroff": true}
	formatCommands    = map[string]bool{"mkfs": true, "mke2fs": true, "mkswap": true, "wipefs": true, "blkdiscard": true, "mkfs.ext2": true, "mkfs.ext3": true, "mkfs.ext4": true, "mkfs.xfs": true, "mkfs.btrfs": true, "mkfs.vfat": true, "mkfs.fat": true, "mkfs.ntfs": true, "mkfs.f2fs": true, "mkfs.exfat": true}
	xargsSensitive    = map[string]bool{"rm": true, "chmod": true, "chown": true, "chgrp": true, "dd": true, "shred": true, "mv": true}
	inlineInterpreter = map[string][]string{"perl": {"-e", "-E"}, "ruby": {"-e"}, "node": {"-e", "--eval", "-p", "--print"}, "php": {"-r"}, "lua": {"-e"}}
)

// isRootish 判断目标是否是根目录 / 家目录 / 系统顶级目录（含 /*、/. 等变体）。
func isRootish(target string) bool {
	t := strings.TrimSpace(target)
	if t == "" {
		return false
	}
	// 去掉 /* /./ 等尾巴：`/etc/*`、`/usr/.`、`~/*`
	for {
		trimmed := strings.TrimSuffix(strings.TrimSuffix(t, "/*"), "/.")
		trimmed = strings.TrimSuffix(trimmed, "*")
		if trimmed == t {
			break
		}
		t = trimmed
	}
	if t == "" {
		// 例如 `/*` → 去尾后为空，等价于根
		return true
	}
	if t == "~" || t == "~/" {
		return true
	}
	if strings.HasPrefix(t, "/") {
		t = path.Clean(t)
	} else if strings.HasPrefix(t, "~/") {
		t = "~" + path.Clean(t[1:])
	}
	return rootishDirs[t]
}

// hasShortFlag 判断参数列表里是否出现某个短选项字母（含 `-rf` 合并写法）或长选项。
func hasFlag(args []word, short byte, long ...string) bool {
	for _, arg := range args {
		text := arg.text
		if text == "--" {
			break
		}
		if strings.HasPrefix(text, "--") {
			for _, l := range long {
				if text == l {
					return true
				}
			}
			continue
		}
		if strings.HasPrefix(text, "-") && len(text) > 1 && strings.IndexByte(text[1:], short) >= 0 {
			return true
		}
	}
	return false
}

// positional 返回非选项参数（`--` 之后全部视为位置参数）。
func positional(args []word) []word {
	var out []word
	for i, arg := range args {
		if arg.text == "--" {
			return append(out, args[i+1:]...)
		}
		if strings.HasPrefix(arg.text, "-") && len(arg.text) > 1 {
			continue
		}
		out = append(out, arg)
	}
	return out
}

func (a *analyzer) checkCall(call *syntax.CallExpr, asPipeReceiver bool) {
	if len(call.Args) == 0 {
		return
	}
	cmd := resolve(call)
	if cmd.nameDynamic {
		a.dynamic("命令名由变量或命令替换产生，无法静态判断")
		return
	}
	if cmd.viaXargs && xargsSensitive[cmd.name] {
		a.dynamic("xargs 的操作目标由上游输出决定，无法静态判断")
	}
	targets := positional(cmd.args)
	switch cmd.name {
	case "rm":
		recursive := hasFlag(cmd.args, 'r', "--recursive") || hasFlag(cmd.args, 'R')
		for _, arg := range cmd.args {
			if arg.text == "--no-preserve-root" {
				a.block("rm-no-preserve-root", "rm 显式关闭根目录保护")
				return
			}
		}
		if !recursive {
			return
		}
		for _, target := range targets {
			if isRootish(target.text) {
				a.block("rm-recursive-root", "递归删除根目录 / 家目录 / 系统顶级目录: "+target.text)
				return
			}
		}
		for _, target := range targets {
			if target.dynamic {
				a.dynamic("递归删除的目标包含变量或命令替换，无法静态判断")
				return
			}
		}
	case "chmod", "chown", "chgrp":
		recursive := hasFlag(cmd.args, 'R', "--recursive")
		if !recursive || len(targets) < 2 {
			return
		}
		for _, target := range targets[1:] { // 第一个位置参数是 mode / owner
			if isRootish(target.text) {
				a.block(cmd.name+"-recursive-root", "递归修改根目录 / 系统顶级目录权限: "+target.text)
				return
			}
			if target.dynamic {
				a.dynamic("递归修改权限的目标包含变量，无法静态判断")
			}
		}
	case "dd":
		for _, arg := range cmd.args {
			if value, ok := strings.CutPrefix(arg.text, "of="); ok && blockDevicePattern.MatchString(value) {
				a.block("dd-to-block-device", "dd 直接写块设备: "+value)
				return
			}
		}
	case "shred":
		for _, target := range targets {
			if blockDevicePattern.MatchString(target.text) {
				a.block("shred-block-device", "shred 擦除块设备: "+target.text)
				return
			}
		}
	case "sgdisk":
		if hasFlag(cmd.args, 'Z', "--zap-all", "--zap") {
			a.block("format-device", "sgdisk 清空分区表")
		}
	case "init", "telinit":
		for _, target := range targets {
			if target.text == "0" || target.text == "6" {
				a.block("power-control", cmd.name+" "+target.text+" 会关机 / 重启")
				return
			}
		}
	case "systemctl":
		for _, target := range targets {
			switch target.text {
			case "poweroff", "reboot", "halt", "kexec", "suspend", "hibernate", "hybrid-sleep":
				a.block("power-control", "systemctl "+target.text+" 会关机 / 重启 / 挂起")
				return
			}
		}
	case "find":
		a.checkFind(cmd.args)
	case "eval":
		a.dynamic("eval 执行动态拼接的字符串，无法静态判断")
	case "su":
		if hasFlag(cmd.args, 'c', "--command") {
			a.dynamic("su -c 内联脚本，无法静态判断")
		}
	case "ssh":
		if len(targets) >= 2 {
			a.dynamic("经 SSH 跳转到其他主机执行命令，超出本连接的审计范围")
		}
	}
	if powerCommands[cmd.name] {
		a.block("power-control", cmd.name+" 会关机 / 重启")
		return
	}
	if formatCommands[cmd.name] || strings.HasPrefix(cmd.name, "mkfs.") {
		a.block("format-device", cmd.name+" 会格式化 / 擦除存储设备")
		return
	}
	if shellNames[cmd.name] {
		if hasFlag(cmd.args, 'c', "--command") {
			a.dynamic(cmd.name + " -c 内联脚本，无法静态判断")
			return
		}
		for _, target := range targets {
			if target.procSubst {
				a.dynamic(cmd.name + " <(...) 执行进程替换产生的脚本")
				return
			}
		}
		if asPipeReceiver && (len(targets) == 0 || hasFlag(cmd.args, 's')) {
			a.dynamic("管道输出直接交给 " + cmd.name + " 执行（curl | sh 类），无法静态判断")
		}
		return
	}
	if strings.HasPrefix(cmd.name, "python") {
		if hasFlag(cmd.args, 'c', "--command") {
			a.dynamic("python -c 内联脚本，无法静态判断")
		}
		return
	}
	if flags, ok := inlineInterpreter[cmd.name]; ok {
		for _, arg := range cmd.args {
			for _, flag := range flags {
				if arg.text == flag || (len(flag) == 2 && strings.HasPrefix(arg.text, "-") && !strings.HasPrefix(arg.text, "--") && strings.IndexByte(arg.text[1:], flag[1]) >= 0) {
					a.dynamic(cmd.name + " 内联脚本，无法静态判断")
					return
				}
			}
		}
	}
}

// checkFind 识别 `find / -delete`、`find / -exec rm ...`。
func (a *analyzer) checkFind(args []word) {
	var paths []string
	deletes := false
	for i, arg := range args {
		text := arg.text
		if strings.HasPrefix(text, "-") || text == "(" || text == "!" {
			if text == "-delete" {
				deletes = true
			}
			if (text == "-exec" || text == "-execdir" || text == "-ok" || text == "-okdir") && i+1 < len(args) && commandName(args[i+1]) == "rm" {
				deletes = true
			}
			continue
		}
		if !deletes && len(paths) == i-countFindOptions(args[:i]) {
			paths = append(paths, text)
		}
	}
	if !deletes {
		return
	}
	for _, p := range paths {
		if isRootish(p) {
			a.block("find-delete-root", "find 从 "+p+" 开始递归删除")
			return
		}
	}
}

// countFindOptions 统计 find 路径之前出现的全局选项（-H/-L/-P/-D/-O）个数，路径必须紧跟其后。
func countFindOptions(args []word) int {
	n := 0
	for _, arg := range args {
		if strings.HasPrefix(arg.text, "-") {
			n++
		}
	}
	return n
}

func (a *analyzer) checkRedirect(redirect *syntax.Redirect) {
	switch redirect.Op {
	case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.RdrClob:
	default:
		return
	}
	target := wordOf(redirect.Word)
	if blockDevicePattern.MatchString(target.text) {
		a.block("redirect-block-device", "重定向输出到块设备: "+target.text)
	}
}

// checkPipeline 检查管道链上的每个接收端是否是「裸 shell」（curl ... | sh / base64 -d | bash）。
func (a *analyzer) checkPipeline(pipe *syntax.BinaryCmd) {
	node := pipe
	for {
		if call, ok := node.Y.Cmd.(*syntax.CallExpr); ok {
			a.checkCall(call, true)
		}
		inner, ok := node.Y.Cmd.(*syntax.BinaryCmd)
		if !ok || (inner.Op != syntax.Pipe && inner.Op != syntax.PipeAll) {
			return
		}
		if call, ok := inner.X.Cmd.(*syntax.CallExpr); ok {
			a.checkCall(call, true)
		}
		node = inner
	}
}

// checkFuncDecl 识别 fork 炸弹：函数体内后台调用自身（`:(){ :|:& };:`）。
func (a *analyzer) checkFuncDecl(decl *syntax.FuncDecl) {
	if decl.Name == nil || decl.Body == nil {
		return
	}
	name := decl.Name.Value
	selfCall, background := false, false
	syntax.Walk(decl.Body, func(node syntax.Node) bool {
		stmt, ok := node.(*syntax.Stmt)
		if !ok {
			return true
		}
		if stmt.Background {
			background = true
		}
		if call, ok := stmt.Cmd.(*syntax.CallExpr); ok && len(call.Args) > 0 && wordOf(call.Args[0]).text == name {
			selfCall = true
		}
		return true
	})
	if selfCall && background {
		a.block("fork-bomb", "函数在后台递归调用自身（fork 炸弹）")
	}
}
