# 部署到 1Panel 云服务器（A / B 两种入口）

这份文档是 `140.143.250.175`（腾讯云 Ubuntu 22.04）上**实际部署过并验证通过**的流程。
两种入口方案都记录，最后说明当前采用的是哪个。

> 与 `deploy/`（自带 Caddy、适合空机器）是两条路，别同时用。

---

## 〇、当前状态（2026-11-11）

| 项 | 值 |
|---|---|
| 部署机 | `140.143.250.175`（Ubuntu 22.04，1Panel 管 openresty） |
| 访问入口 | **方案 B**：`https://sync.tx.bestuav.cn` → `.26`（39.101.161.26）→ `.175:17420` |
| 服务位置 | `.175` 的 Docker 容器 `atlas-sync`，数据在 `~/atlas-sync/data/` |
| 客户端地址 | `https://sync.tx.bestuav.cn`（不带 `/v1`） |
| 管理员令牌 | `.175` 上 `~/atlas-sync/.env` 里的 `ATLAS_SYNC_ADMIN_TOKEN` |

**为什么最终选 B**：证书与站点已经在 `.26` 上配好（DigiCert/阿里云 DV 都在那台），
把 DNS 指到 `.26` 只要改一条记录；而方案 A 要再去 `.175` 的 1Panel 里上传证书。
代价是 `.26 → .175` 有一跳明文（见文末"安全权衡"）。

---

## 一、事实核对（先看，别凭感觉）

这台机器的情况决定了方案怎么写：

| 事实 | 影响 |
|---|---|
| 80/443 被 **1Panel 的 openresty** 占着（`network_mode: host`） | 不能再起 Caddy/Nginx 抢端口 |
| `wangss` 在 `docker` 组，但 **sudo 需要密码** | 部署走 Docker，不碰系统目录 |
| `~/atlas-sync/data` 属主是 `wangss`（uid 1002） | 容器必须用 `user: "1002:1002"`，否则 SQLite 写不进去 |
| `bestuav.cn` 的 NS 在阿里云（`dns3/dns4.hichina.com`） | 免费 DV 证书可自动 DNS 验证 |
| `bestuav.cn` 解析到 `.26`；`*.tx.bestuav.cn` 解析到 `.175` | 两个域名空间分属两台机器，别配串 |
| 两台机器各有一个 1Panel | **在 `.26` 上配 `sync.tx` 的 HTTPS 不会生效**（DNS 指到 `.175` 时） |

---

## 二、编译与起容器（A/B 共用）

### 1. 交叉编译（开发机）

```powershell
cd bestuav-atlas
$env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
go build -trimpath -ldflags "-s -w -X main.version=0.11.0" -o build/atlas-sync-linux-amd64 ./cmd/atlas-sync
```

静态二进制约 11MB（modernc sqlite 不需要 cgo）。

### 2. 上传（开发机）

```bash
scp -P <ssh端口> build/atlas-sync-linux-amd64 <用户>@<主机>:~/atlas-sync/atlas-sync
scp -P <ssh端口> deploy/vps/Dockerfile deploy/vps/docker-compose.yml <用户>@<主机>:~/atlas-sync/
```

### 3. 令牌与启动（服务器）

```bash
cd ~/atlas-sync
chmod +x atlas-sync
mkdir -p data
# 生成管理员令牌（32 位十六进制），写进 .env 并收紧权限
printf 'ATLAS_SYNC_ADMIN_TOKEN=%s\n' "$(openssl rand -hex 16)" > .env
chmod 600 .env
id -u; id -g          # 把 compose 里的 user 改成这两个数字
docker compose up -d --build
curl -s http://127.0.0.1:17420/v1/health
```

到这一步服务已经能在**服务器本机**访问了，剩下的只是"从公网怎么进来"。

---

## 三、方案 A：DNS 直连部署机（推荐长期）

```
客户端 → https://sync.tx.bestuav.cn（A 记录 → 140.143.250.175）
      → .175 的 openresty（1Panel 站点 sync.tx.bestuav.cn）
      → http://127.0.0.1:17420（容器，只绑回环）
```

配置要点：

1. **DNS**：`sync` 记录（主机记录填 `sync.tx`）→ `140.143.250.175`；
2. **compose**：端口用 `"127.0.0.1:17420:7420"`（不对公网暴露）；
3. **1Panel（.175）**：站点 `sync.tx.bestuav.cn` 的反代目标 `http://127.0.0.1:17420`；
4. **证书**：给这个站点绑一张覆盖 `sync.tx.bestuav.cn` 的证书（阿里云免费 DV → Nginx 格式 → 上传 → **绑定到站点**）；
5. 勾「强制 HTTPS」。

优点：全程 TLS、只一跳、17420 不外露。
缺点：证书要装在 `.175` 上，且这台机器的 1Panel 不能直接申请 ACME，必须手工上传。

---

## 四、方案 B：`.26` 做统一入口（**当前采用**）

```
客户端 → https://sync.tx.bestuav.cn（A 记录 → 39.101.161.26）
      → .26 的 openresty（证书在这里）
      → http://140.143.250.175:17420（跨云一跳，明文）
      → atlas-sync
```

配置要点：

1. **DNS**：`sync.tx` → `39.101.161.26`；
2. **compose**：端口必须 `"17420:7420"`（监听所有网卡）——
   写成 `127.0.0.1:17420:7420` 的话 `.26` 连不上，表现为 **502 Bad Gateway**；
3. **腾讯云安全组（.175）**：放行 `TCP:17420`，来源限制 `39.101.161.26/32`（别开 0.0.0.0/0）；
4. **1Panel（.26）**：站点 `sync.tx.bestuav.cn` 反向代理到 `http://140.143.250.175:17420`，
   绑证书并勾「强制 HTTPS」；
5. `.175` 上原来那个 `sync.tx.bestuav.cn` 反代站点**不参与链路**，建议删掉避免误判。

优点：复用 `.26` 已有的证书体系，改一条 DNS 即可。
缺点：17420 对外（需靠安全组收窄），且 `.26 → .175` 明文。

---

## 五、管理员令牌从哪来

**不是**在 App 里生成，也**不是**随便填的：

1. 部署时在服务器上生成：`openssl rand -hex 16` → 32 位十六进制；
2. 写进 `~/atlas-sync/.env`（`chmod 600`），由 compose 的 `env_file` 注入容器；
3. 服务端只把它当"管理接口密码"：用于**桌面端「连接」**（登记设备）和**「生成配对码」**；
4. 日常同步用的是**设备令牌**（配对时换到的），与管理员令牌是两套凭据，权限不同。

查看当前令牌：

```bash
grep ATLAS_SYNC_ADMIN_TOKEN ~/atlas-sync/.env
```

换令牌：

```bash
printf 'ATLAS_SYNC_ADMIN_TOKEN=%s\n' "$(openssl rand -hex 16)" > ~/atlas-sync/.env
cd ~/atlas-sync && docker compose up -d        # 重建容器使新值生效
```

> 换管理员令牌**不会**让已配对的手机失效（设备令牌是另一套，服务端只存其 SHA-256）。

---

## 六、客户端怎么填

| 端 | 地址 | 凭据 |
|---|---|---|
| 桌面 | 设置 → 同步 → `https://sync.tx.bestuav.cn` | 管理员令牌（点「连接」） |
| 手机 | 配对页 → 同一地址 + 8 位配对码 | 配对码在桌面「生成配对码」 |

手机端从 0.2.0 起**只认 HTTPS**（清单里没有 `usesCleartextTraffic`，
`SyncApi` 也会拒绝 `http://`），所以不要再填内网 IP。

---

## 七、验证与排障

```bash
# 服务本身
curl -s http://127.0.0.1:17420/v1/health

# 公网（按方案 A 或 B 的域名）
curl -s https://sync.tx.bestuav.cn/v1/health
```

| 现象 | 原因 | 处理 |
|---|---|---|
| 502 Bad Gateway | ① 容器只绑了 `127.0.0.1`（方案 B）；② 上游地址写错；③ 安全组没放行 17420 | 看 `docker port`；看 1Panel 上游；看安全组 |
| `tlsv1 alert internal error` | 443 上没有匹配该域名的 server 块（证书没绑定成功） | `grep listen /opt/1panel/.../conf.d/<站点>.conf`，应有 `listen 443 ssl` |
| 浏览器/curl 证书名不匹配 | 绑成了 `bestuav.cn` 的证书 | 站点必须绑 `sync.tx.bestuav.cn` 的证书 |
| HTTP 通、HTTPS 不通 | 只做了 DNS，没装证书 | 见第三/第四节 |
| 容器反复重启，日志 `unable to open database file (14)` | bind mount 的 `./data` 属主与容器 uid 不一致 | compose 加 `user: "<uid>:<gid>"` |
| 手机「连不上同步服务」但浏览器能开 | release 包缺 `INTERNET` 权限（Flutter 模板只放在 debug 清单里） | `src/main/AndroidManifest.xml` 补 `INTERNET` |

---

## 八、备份 / 升级 / 卸载

```bash
# 备份（数据就一个 sqlite 文件）
cd ~/atlas-sync && docker compose stop atlas-sync
tar czf atlas-sync-$(date +%F).tar.gz data
docker compose start atlas-sync

# 升级：开发机重编 → scp 覆盖 → 重建
scp atlas-sync-linux-amd64 <用户>@<主机>:~/atlas-sync/atlas-sync
ssh <主机> 'cd ~/atlas-sync && docker compose up -d --build'

# 卸载（先备份 data）
docker compose down
```

库结构迁移在启动时自动执行；迁移失败会直接退出，不会带病运行。

---

## 九、安全权衡（方案 B 的已知代价）

- `.26 → .175` 是明文 HTTP：待办内容会经过公网（同属腾讯云）。
- 缓解：安全组把 17420 的来源锁死到 `.26`；后续若在意，按**方案 A** 切回去
  （DNS 指回 `.175` + 在 `.175` 装同一张证书），把这一跳也变成 TLS 且不再暴露端口。
- 管理员令牌是"root 级"凭据：只放 `.env`（600），不要写进仓库或聊天记录。

## 十、特殊说明
### 域名相关知识

- 域名在哪个服务商买的，就用哪个服务商的DNS解析：比如域名在阿里云买的，就用阿里云的“云解析 DNS”；在腾讯云买的，就用“DNSPod”。你需要登录对应平台的控制台，找到 bestuav.cn，进入“解析设置”，添加记录。

### 添加记录

- 主机记录：填 @（代表 bestuav.cn 本身）或 www（代表 www.bestuav.cn）
- 记录类型：选 A。
- 记录值：填你目标服务器的公网 IPv4 地址（比如 39.101.161.26）。
- 线路类型：通常选“默认”，否则部分地区的用户可能无法访问。

### 是否需要开防火墙

- 如果阿里云上配置DNS解析时直接指向 腾讯云的公网ip，然后通过腾讯云上的1panel反向代理找服务，则不需要开防火墙
- 如果阿里云上配置DNS解析时直接指向 阿里云的公网ip，然后通过阿里云中的1panel反向代理到腾讯云则需要在腾讯云上开防火墙，腾讯云不需要再次反向代理了