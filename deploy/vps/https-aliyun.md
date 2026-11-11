# HTTPS：阿里云免费证书 + 1Panel 上传

适用：服务器由 1Panel 管理、但**不能/不想用 1Panel 的 ACME**，证书统一从阿里云签发。

本文以 `sync.tx.bestuav.cn → 140.143.250.175` 为例。`bestuav.cn` 的 NS 在阿里云
（`dns3/dns4.hichina.com`），所以 DNS 验证可以自动完成，不用手工加 TXT。

## 一、阿里云申请（约 3 分钟）

1. 阿里云控制台 → **数字证书管理服务（SSL 证书）** → **免费证书** → **创建证书**；
   - 免费额度：每账号每年 20 张，单域名，**有效期 3 个月**（到期必须重签）；
2. 域名填 `sync.tx.bestuav.cn`（**单域名证书可以签多级子域**，不需要泛域名）；
3. 验证方式：**DNS**（域名在本账号阿里云 DNS 下时会自动加 TXT，通常 1–5 分钟签发）；
   若提示需要手工加记录，去「云解析 DNS」加一条 `_dnsauth.sync.tx` 的 TXT 即可；
4. 签发后在证书列表点 **下载** → 选 **Nginx** → 得到两个文件：
   - `sync.tx.bestuav.cn.pem`（**包含中间证书**，顺序是 域名证书 → 中间证书）
   - `sync.tx.bestuav.cn.key`

> 不要用「Apache」或「IIS」格式：那两个格式的 pem 内容组织不同，直接粘进 nginx 会缺链。

## 二、1Panel 上传（约 1 分钟）

1. 网站 → `sync.tx.bestuav.cn` → **HTTPS**（有的版本叫「证书」）；
2. **SSL 选项** 选 **「选择已有证书」** → **「手动创建」**（或「上传证书」）；
3. 两个输入框分别粘贴：
   - **私钥**：`-----BEGIN PRIVATE KEY-----` 开头的 `.key` 全文（RSA 也可能是 `BEGIN RSA PRIVATE KEY`）；
   - **证书**：`.pem` 全文（**必须带中间证书**，两段 `BEGIN CERTIFICATE` 都要）；
4. 保存 → 打开 **「强制 HTTPS」**（或「HTTP 自动跳转到 HTTPS」）。

保存后 1Panel 会自动生成 `listen 443 ssl` 的 server 块并 reload openresty。

## 三、验证

```bash
curl -s https://sync.tx.bestuav.cn/v1/health
# 期望：{"version":"0.11.0","schema":"1","protocol":1,"devices":0,...}

# 看证书链与实际签发的域名
echo | openssl s_client -connect sync.tx.bestuav.cn:443 -servername sync.tx.bestuav.cn 2>/dev/null \
  | openssl x509 -noout -subject -issuer -dates -ext subjectAltName
```

浏览器打开 `https://sync.tx.bestuav.cn/v1/health` 应显示小锁，无证书警告。

## 四、常见问题

| 现象 | 原因 / 处理 |
|---|---|
| `tlsv1 alert internal error` | 443 上没有匹配该域名的 server 块（证书没保存成功）。检查 `conf.d` 里是否出现了 `listen 443` 的 `sync.tx.bestuav.cn` 块 |
| 浏览器报「证书不完整 / 链不完整」 | `.pem` 只粘了域名证书，漏了中间证书。Nginx 格式的 pem 里应有 **两段** `BEGIN CERTIFICATE` |
| 报 `key values mismatch` | 私钥和证书不是同一次申请下的（重新下载配对的两个文件） |
| 80 正常、443 拒绝 | 1Panel 里没点「强制 HTTPS」不影响 443，看是否 SSL 未启用；或 openresty 未 reload |
| 证书 90 天后过期 | 免费证书 3 个月。到期前重签并重复第二步；建议在日历里设提醒，或在 1Panel 证书备注里写到期日 |

## 五、证书装好之后

1. **关掉公网 17420**：反代走 `127.0.0.1:17420`，不需要对外端口；
   去腾讯云「安全组」把 17420 的入站规则删掉（本机 `ufw` 若开启也一并关）。
2. 客户端统一用 `https://sync.tx.bestuav.cn`（不带 `/v1`）；
3. 此时可以把 `bestuav-app` 的 `android:usesCleartextTraffic="true"` 去掉，
   回到「只许 HTTPS」；手机端本地开发仍可用 DevCore（不联网）。

## 六、为什么不直接让 1Panel 申请 ACME

本机的 1Panel 版本没有可用的 ACME 申请入口（或账户/网络不通），而阿里云免费 DV
证书在**同一账号的 DNS 下可以自动验证**，比在服务器上折腾 ACME 客户端更稳，
也方便以后把签发/续期统一到阿里云。
