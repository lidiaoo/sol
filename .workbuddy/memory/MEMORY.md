# sol 项目长期笔记

## 本机怎么编译 / 测试
- 系统 `go` 是 **gccgo 1.18**，模块要 1.26 → 用 `/home/jianghh/sdk/go1.26.8/bin/go`。
- `HOME=/root` 不可读、`/tmp` 是 10MB tmpfs → 必须显式给缓存，否则报 `no space left on device`
  或 `could not create module cache`：
  `GOTOOLCHAIN=local GOMODCACHE=/home/jianghh/go/pkg/mod GOCACHE=<可写目录> TMPDIR=<可写目录>`。
- 全量测试：`go test -tags mock -race -cover ./...`（等价于 `make test`）。
- 沙箱里绑不了 <1024 端口：`sol listen --config <带 10/11/12 端口的配置>` 会停在
  `bind: permission denied`。那是环境限制，不是配置错误，配置能否加载看它前面几行日志。
- `http control plane listening` 是 **go 协程**里打的，主路径快速失败时可能来不及出现——别拿它
  的有无来判断配置是否开启，用加载器或 `TestZZProbeExample` 那种写法看 `cfg.HTTP.Enabled`。

## 改配置字段 / 改默认配置时必须一起改的地方
1. `internal/config/schema.go` + `internal/config/load.go`（+ `load_internal_test.go`）
2. `schema/sol.schema.json`：`TestSchemaMirrorsTheConfigStructs` 拿 yaml tag 与属性名做
   ElementsMatch，缺一个就红
3. `scripts/install.sh`（heredoc）与 `scripts/install.ps1`（行数组）：两份模板必须逐行一致
4. `example/sol-example.yaml`；`README.md` + `README.en.md`：`TestReadmeTranslationsAgree` 要求
   两份 README 的代码块**逐字节一致**
5. `CHANGELOG.md` 的 `[Unreleased]` 与 `docs/routing-design.md` 的字段表

## 令牌 / 密钥约定
- `server.http.auth` 的三个来源 `token` / `token_env` / `token_file` **只准写一个**，多写启动即报
  `ErrSecret`。用户明确要互斥，不要改成"按优先级取第一个"。
- `token_file` 的 0600 检查在 Windows 上必然失败（读的是 POSIX 权限位），这是**决定不修**的
  （见 `docs/install-pitfalls.md` 第 10 条）。
- 安装脚本生成的配置带明文令牌：POSIX 上文件 0600；Windows 上 `icacls` 只留 SYSTEM 与
  Administrators。已存在的配置**绝不覆盖**，所以老安装不会自动拿到新模板。

## 风格与沟通
- 脚本注释、错误信息、日志一律中文；README 有中英两份，代码块必须同步。
- 用户偏好最小改动、最简方案；**没有要求的 UI 文案不要动**，拿不准就先问（他通常选"推荐"那项）。
