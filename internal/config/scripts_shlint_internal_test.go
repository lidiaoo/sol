package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestShellScriptsBraceVariablesBeforeNonASCII 守住一条 macOS 铁律。
//
// macOS 自带的 /bin/sh 是 bash 3.2，没有多字节感知：只要变量名后面紧跟一个非 ASCII 字节（中文标点、
// 破折号都算），bash 会把那个字节也算进变量名。于是 `$want_arch）` 实际取的是 `want_arch\xef`，在
// `set -u` 下当场 unbound variable，整只安装/发布脚本一句都跑不出来。Linux 的 bash 5 不会这样，所以
// 这类错误只在 macOS 真机上暴露。
//
// 规矩：脚本里变量后面紧跟非 ASCII 字符时，一律写成 ${VAR}。这个测试就是让这条规矩有牙齿。
func TestShellScriptsBraceVariablesBeforeNonASCII(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "scripts", "*.sh"))
	if err != nil || len(files) == 0 {
		t.Fatalf("没找到 scripts/*.sh（%v，%d 个）", err, len(files))
	}
	re := regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)`)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
		for i, line := range lines {
			for _, m := range re.FindAllStringSubmatchIndex(line, -1) {
				if m[0] > 0 && line[m[0]-1] == '{' {
					continue // 已经是 ${VAR}
				}
				if e := m[1]; e < len(line) && line[e] >= 0x80 {
					t.Errorf("%s:%d: 变量 $%s 后面紧跟非 ASCII 字节；macOS 的 bash 3.2 会把它算进变量名，"+
						"在 set -u 下直接 unbound variable。改成 ${%s}：%s",
						filepath.Base(f), i+1, line[m[2]:m[3]], line[m[2]:m[3]], strings.TrimSpace(line))
				}
			}
		}
	}
}
