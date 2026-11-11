# 同步服务部署（云主机）

星图的跨设备同步是**自建**的：一台机器跑 `atlas-sync`，桌面与手机都连它。
不需要数据库服务、不需要域名备案以外的东西，只有一个 SQLite 文件和两个端口。

## 一、前置

- 一台有公网 IP 的 Linux 云主机（1 核 1G 足够，同步请求都是几百字节的 JSON）；
- 一个已解析到它的域名（Caddy 要用它签发证书）；没有域名就用 `http://IP:7420`
  直接跑，**但公网明文传输令牌不安全**，只建议在内网/本机这么用；
- Docker + Docker Compose 插件。

## 二、起服务

```bash
cd bestuav-atlas/deploy
cp .env.example .env

# 生成管理员令牌（只生成一次，丢了就得重置：删 data/ 重新配对）
openssl rand -hex 16        # 填到 .env 的 ATLAS_SYNC_ADMIN_TOKEN
# 填 .env 的 SYNC_DOMAIN

docker compose up -d --build
docker compose logs -f atlas-sync
```

验证：

```bash
curl -s https://sync.example.com/v1/health
# {"version":"0.11.0","schema":"1","protocol":1,"devices":0,"ops":0,"seq":0,...}
```

防火墙只放 22 / 80 / 443；`7420` 不要映射到宿主机（compose 里已经只 `expose`）。

## 三、接桌面端

1. 打开星图 → 设置 → **同步**；
2. 填服务地址（`https://sync.example.com`）与**管理员令牌**；
3. 点「连接」：桌面会登记成一台设备、换到设备令牌，并立刻跑一轮同步。

## 四、接手机（M2 之后）

1. 桌面 → 设置 → 同步 → **生成配对码**（8 位、5 分钟、一次性）；
2. 手机端输入「服务地址 + 配对码」→ 换到自己的设备令牌；
3. 手机端首次会拉快照，之后走增量。

> 配对码是给手机用的，桌面**不要**用它；桌面走管理员令牌直接登记。

## 五、备份与迁移

数据全在 `deploy/data/atlas-sync.db`（含 `-wal`/`-shm`）：

```bash
docker compose stop atlas-sync
tar czf atlas-sync-$(date +%F).tar.gz -C deploy data
docker compose start atlas-sync
```

或在线做一致性快照（SQLite 的 `VACUUM INTO`，不锁读）：

```bash
docker compose exec atlas-sync sh -c \
  'wget -qO- http://127.0.0.1:7420/v1/health'   # 先确认它还活着
# 真正的在线备份目前用「停容器拷贝」：见上一条，最稳。
```

> 目前没有内置备份子命令：停容器拷贝最稳。**op 表永不清理**（墓碑除外），
> 所以任何一台设备坏了都能从服务端重放重建。

## 六、升级

```bash
git pull
docker compose up -d --build      # 重建镜像；库结构迁移在启动时自动完成
```

`/v1/health` 的 `schema` 与 `protocol` 是兼容性判据：桌面/手机连接时会比对
`protocol`，不一致会直接提示「请升级一端」，而不是静默写坏数据。

## 七、排障

| 现象 | 先看什么 |
|---|---|
| 桌面「连接失败：请求失败」 | 地址写错 / 证书没签下来（`docker compose logs caddy`） |
| 「管理员令牌无效」 | `.env` 里改过但容器没重启；或令牌带空格 |
| 「配对码无效/已过期」 | 码 5 分钟过期且一次性；重新生成 |
| 同步成功但另一台看不到 | 两台设备是否连的**同一个** `SYNC_DOMAIN`；`/v1/health` 的 `seq` 是否在涨 |
| 手机在 4G 下连不上 | 域名解析/防火墙；同步服务没走内网，别用 `192.168.x.x` |
