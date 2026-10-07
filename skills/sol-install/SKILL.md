---
name: sol-install
description: Use when installing or removing sol on any platform.
version: 1.0.0
author: Hermes Agent
license: MIT
metadata:
  hermes:
    tags: [sol, install, linux, macos, windows, systemd, launchd, schtasks]
    related_skills: [systematic-debugging]
---

# 装 sol（Linux / macOS / Windows）

## When to Use

- 在任一台机器上装、升级、看现状、卸载 sol。
- 用户报"装不上 / 任务判断不准 / 进程判断不准 / 日志不知道在哪"（先读下面两条"状态块"的规矩，多半就出在那里）。
- 要改 `scripts/install.sh` / `install.ps1` / `install.cmd`，或它的 spec、冒烟、CI。

安装体验归**安装脚本**，不归 sol 二进制：不要给 sol 加 `install` / `status` 之类的子命令。

## 契约（用户定的，别改）

- 脚本**零参数**，选项用**编号**回答，回车＝1：`1` 按配置应用 / `2` 卸载 / `3` 重新生成 `install.yaml` / `4` 退出。
- 安装配置只有 `run.args` 一项（用户已经有可执行文件）。
- **只动自己建的东西**：认台账（`install.json`）与落点路径，绝不按进程名或服务名去猜。
- 先"报现状"再"问"；选 `4` 绝不改动任何东西。
- 现状块与完成报告都要打出**日志去处**（Linux `journalctl -u sol.service -f`、macOS `/usr/local/var/log/sol.log`、Windows `<安装目录>\sol.log`），台账里也记 `log_paths`。

## 状态块必须在**未升权**时也读得准（最大的坑）

现状是在升权**之前**打印的，而服务以 root / SYSTEM 跑。把"读不到"当成"没有"是这个项目反复踩的坑：

- **任务/服务在不在**：Windows 用 `Get-ScheduledTask`（退路：`schtasks /Query` 全量列表再按名字精确匹配）。**绝不用 `schtasks /Query /TN <name>` 判存在**——任务不在时它回的是"拒绝访问"（退出码 1），于是权限问题被误报成"不存在"。
- **进程**：先按**可执行文件路径**认（Windows 走 CIM `Win32_Process` 的 `ExecutablePath`/`CommandLine`；Linux 读 `/proc/<pid>/cmdline` 的 argv[0]；**不要** `readlink /proc/<pid>/exe`，非 root 读 root 进程会被内核拒）。认不出时按名字给一个**带说明**的降级结论（"有 N 个 sol.exe 在跑；没升权时认不出是不是安装目录那份"），别直接说"没有在跑"。
- 结论要**三态**：installed / absent / **unknown（看不清，就说"以管理员身份重跑一次才能看清"）**。
- 台账与现实的矛盾要**说出来**：台账写着 `created_unit: true` 而现在查不到 → 直白告诉用户"被删过、或被清理工具动过；重跑选 1 会再建"。

## 说到的必须复核

- 建完服务/任务要**再查一次**；查不到就把台账记成 `incomplete` 并显著报警。不能让"注册失败"以 `incomplete: false` 蒙混过去——真机上就是这么把"任务压根不在"报成了安装成功。
- 报告里的等价命令要写我们**真正**跑的那条（改实现就改那行，别留旧命令）。

## 升级前要停"服务单元"，不是进程

systemd 的 `Restart=` / launchd 的 `KeepAlive` 会在毫秒级把进程拉回来，端口又被占，预检第二次照样失败、升级卡死在"端口占用"。做法：先**软停单元**（`systemctl stop` / `launchctl bootout`），预检仍不过就按原样起回去（开机自启不能丢）。

## Windows：任务动作必须一眼看得懂

- 计划任务**直接跑 `sol.exe` + 参数**，不要包一层 `run-sol.cmd`——任务计划里只看到一个 `.cmd`，用户不认。现状块再照抄一行 `启动  <exe> <参数>`（取自 `Get-ScheduledTask` 的 `Actions[0].Execute` / `.Arguments`）。
- 计划任务收集不到 stdout → 让 sol 自己写文件：往运行配置里补 `logging: { output: file, file: <安装目录>\sol.log }`。**只在没有 `logging:` 一节时补**（用户自己写的一律不动），并用**不带 BOM 的 UTF-8** 追加（PS 5.1 的 `-Encoding utf8` 会插 BOM，往文件中间插 BOM 会让 YAML 读不动）。
- 更多细节与其它雷见 `references/windows-task-scheduler.md`。

## 编码 / 可移植性铁律

- sh 脚本按 POSIX 写：**变量紧跟中文必须写 `${VAR}`**——macOS 自带 bash 3.2 会把变量名解析到全角括号的首字节，`set -u` 下整只脚本当场死。
- 读 `/proc` 里的文件要让**命令**去读（`head -c 4096 "$f" 2>/dev/null`），别用 shell 的 `<` 重定向：重定向失败是 **shell 自己**报的错，挂在命令上的 `2>/dev/null` 压不住。
- `install.ps1` 必须保持 **UTF-8 BOM**：PS 5.1 把无 BOM 的 `.ps1` 当 ANSI（中文系统 GBK）读，中文会把引号搞乱、整只脚本解析失败。
- 改 `.ps1` 前先确认行尾：Windows 检出常是 **CRLF**，锚点要么带 `\r`，要么规范化读写后写回同种行尾。

## 怎么验

见 `references/testing-lanes.md`：真 Windows（本机就是）/ 真 Linux（qemu 纯软件模拟，真 systemd）/ 逻辑级 PowerShell 冒烟（便携 pwsh + 函数桩）/ CI 三平台 matrix。每修一个"只在真机现形"的 bug，就往对应通道加一条回归断言。
