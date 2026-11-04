# bestuav-atlas

星图 —— 基于 mygo 的纯 Go 原生桌面应用，由 bestuav-mygo 脚手架生成。

## 快速开始

```bash
go run .                # 直接跑起来
go tool mygo dev        # 带前端热重载的开发模式
tasklist                # 本机预览用的命令在 Taskfile 里（若存在）
```

## 目录结构

| 路径 | 作用 |
| --- | --- |
| `main.go` | 进程入口：日志 → 数据库 → 单实例锁 → 窗口与托盘 |
| `internal/view/registry.go` | **菜单与页面的唯一注册处**，加菜单只改这里 |
| `internal/appmeta/appmeta.go` | 产品标识常量（名称 / 品牌 / 反向域名标识） |
| `internal/settings` | 用户偏好持久化（settings.json） |
| `internal/store` | 本地 SQLite |
| `internal/logging` | 结构化日志与事件入库 |
| `scripts/` | 本地打包脚本（Windows / macOS / Linux） |
| `.github/workflows/release.yml` | 推 tag 即自动打三平台安装包 |

## 下一步做三件事

1. **加业务页面**：写一个 `func (a *App) orderPage(c *ui.Context, t *ui.Theme)`，
   再到 `internal/view/registry.go` 的 `navRegistry()` 里登记一条 `navItem`。
2. **改品牌**：启动后进「设置 → 品牌」直接改名（侧栏与窗口标题即时生效）；
   要改安装包名与内部标识，动 `internal/appmeta/appmeta.go` 与 `mygo.json` 后重新打包。
3. **发版**：见 [`docs/首次发布.md`](docs/首次发布.md)——里面有更新密钥怎么配、
   GitHub Secret 怎么填、tag 怎么打、客户端怎么收到更新。

## 推送代码

远程已配好但尚未推送（推送要用你自己的凭据）：

```bash
git push -u origin main
```

远端地址：`https://github.com/lark203/bestuav-atlas.git`

## 数据与日志

配置、数据库与日志都在系统用户配置目录下（Windows 为 `%APPDATA%\bestuav-atlas\`）。
启动后「设置 → 关于 → 基本信息」会显示实际路径，并提供打开目录的按钮。
