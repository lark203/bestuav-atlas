// Package deploy 把「把 atlas-sync 发到自己的服务器上」这件事做成纯 Go 的几步：
// 交叉编译 → SFTP 上传 → 远端写 compose/Dockerfile → 重建容器 → 健康检查。
//
// 为什么放在桌面端而不是继续写脚本：脚本要记参数、记路径、记 SSH 端口，
// 每换一台机器就得重抄一遍；界面里填一次就能一直点。
//
// 安全约定：
//   - 只支持私钥登录（不存密码）；
//   - 主机指纹不校验（HostKeyCallback 忽略），token/密钥都不写进配置文件导出；
//   - 远端凭据只存在本机 settings 里，随 profile 导出时会被剔除（见 settings.machineKeys）。
package deploy

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"bestuav-atlas/internal/appmeta"
)

// Target 一台部署目标（SSH + 远端目录）。
type Target struct {
	Host string // 主机名或 IP
	Port int    // SSH 端口，0 视为 22
	User string // 登录用户
	Key  string // 私钥文件路径（~/.ssh/id_rsa）
	Dir  string // 远端目录，如 /home/wangss/atlas-sync
	Src  string // 本机 atlas 源码目录（含 cmd/atlas-sync）
}

// VersionTag 远端镜像标签，取模块版本（与 /v1/health 显示的一致）。
var VersionTag = appmeta.Version()

// Validate 检查必填项，返回给界面直接显示的错。
func (t Target) Validate() error {
	switch {
	case strings.TrimSpace(t.Host) == "":
		return fmt.Errorf("先填服务器地址")
	case strings.TrimSpace(t.User) == "":
		return fmt.Errorf("先填 SSH 用户")
	case strings.TrimSpace(t.Key) == "":
		return fmt.Errorf("先填私钥路径")
	case strings.TrimSpace(t.Dir) == "":
		return fmt.Errorf("先填远端目录")
	case strings.TrimSpace(t.Src) == "":
		return fmt.Errorf("先填源码目录")
	}
	if _, err := os.Stat(t.Key); err != nil {
		return fmt.Errorf("读不到私钥 %s：%w", t.Key, err)
	}
	if _, err := os.Stat(filepath.Join(t.Src, "cmd", "atlas-sync")); err != nil {
		return fmt.Errorf("%s 不像 atlas 源码目录（缺 cmd/atlas-sync）", t.Src)
	}
	return nil
}

// Client 一条已建立的 SSH 连接（带 SFTP）。
type Client struct {
	conn *ssh.Client
	sftp *sftp.Client
	home string // 远端 $HOME，用来展开 SFTP 路径里的 ~
	done bool
}

// Dial 用私钥建连。hostkey 不校验：部署目标是自己的机器，换来的是免去首次指纹确认。
func Dial(t Target) (*Client, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(t.Key)
	if err != nil {
		return nil, fmt.Errorf("读私钥失败: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		if _, ok := err.(*ssh.PassphraseMissingError); ok {
			return nil, fmt.Errorf("私钥有口令保护，暂不支持；请用无口令的部署专用密钥")
		}
		return nil, fmt.Errorf("解析私钥失败: %w", err)
	}
	port := t.Port
	if port == 0 {
		port = 22
	}
	cfg := &ssh.ClientConfig{
		User:            t.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}
	conn, err := ssh.Dial("tcp", net.JoinHostPort(t.Host, strconv.Itoa(port)), cfg)
	if err != nil {
		return nil, fmt.Errorf("SSH 连接失败: %w", err)
	}
	sc, err := sftp.NewClient(conn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("SFTP 初始化失败: %w", err)
	}
	c := &Client{conn: conn, sftp: sc}
	// SFTP 不认 ~，先问一次 $HOME；失败也不致命（用绝对路径时用不到）。
	if out, err := c.Output(context.Background(), "echo $HOME"); err == nil {
		c.home = strings.TrimSpace(out)
	}
	return c, nil
}

// Close 关闭连接（幂等）。
func (c *Client) Close() {
	if c == nil || c.done {
		return
	}
	c.done = true
	_ = c.sftp.Close()
	_ = c.conn.Close()
}

// Run 在远端执行一条命令，返回合并输出；退出码非 0 时返回 error（带输出末尾）。
func (c *Client) Run(ctx context.Context, cmd string) (string, error) {
	sess, err := c.conn.NewSession()
	if err != nil {
		return "", fmt.Errorf("打开会话失败: %w", err)
	}
	defer func() { _ = sess.Close() }()

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = sess.Signal(ssh.SIGKILL)
		case <-done:
		}
	}()
	defer close(done)

	out, err := sess.CombinedOutput(cmd)
	text := string(out)
	if err != nil {
		return text, fmt.Errorf("命令失败（%s）: %w", tail(text, 300), err)
	}
	return text, nil
}

// Output 同 Run，但只要标准输出（用于 curl 健康检查这种要解析结果的场景）。
func (c *Client) Output(ctx context.Context, cmd string) (string, error) {
	sess, err := c.conn.NewSession()
	if err != nil {
		return "", fmt.Errorf("打开会话失败: %w", err)
	}
	defer func() { _ = sess.Close() }()
	out, err := sess.Output(cmd)
	if err != nil {
		return string(out), fmt.Errorf("命令失败: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Resolve 把远端路径里的 ~ 展开成 $HOME（SFTP 不会自己展开）。
func (c *Client) Resolve(dir string) string {
	dir = strings.TrimSpace(dir)
	if c.home == "" {
		return dir
	}
	if dir == "~" {
		return c.home
	}
	if strings.HasPrefix(dir, "~/") {
		return c.home + dir[1:]
	}
	return dir
}

// shellQuote 给路径套单引号，避免空格/特殊字符把命令拆坏。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Upload 把本地文件传到远端（自动建父目录，权限 0755）。
func (c *Client) Upload(local, remote string) error {
	if err := c.sftp.MkdirAll(path.Dir(remote)); err != nil {
		return fmt.Errorf("建远端目录失败: %w", err)
	}
	src, err := os.Open(local)
	if err != nil {
		return fmt.Errorf("打开本地文件失败: %w", err)
	}
	defer func() { _ = src.Close() }()

	dst, err := c.sftp.Create(remote)
	if err != nil {
		return fmt.Errorf("创建远端文件失败: %w", err)
	}
	n, err := io.Copy(dst, src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("上传中断（已写 %d 字节）: %w", n, err)
	}
	_ = c.sftp.Chmod(remote, 0o755)
	return nil
}

// WriteFile 在远端写一个小文本文件（compose/Dockerfile 这种）。
func (c *Client) WriteFile(remote, content string, mode os.FileMode) error {
	if err := c.sftp.MkdirAll(path.Dir(remote)); err != nil {
		return fmt.Errorf("建远端目录失败: %w", err)
	}
	dst, err := c.sftp.Create(remote)
	if err != nil {
		return fmt.Errorf("创建远端文件失败: %w", err)
	}
	_, werr := io.WriteString(dst, content)
	if cerr := dst.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("写远端文件失败: %w", werr)
	}
	_ = c.sftp.Chmod(remote, mode)
	return nil
}

// Exists 远端路径是否存在。
func (c *Client) Exists(remote string) bool {
	_, err := c.sftp.Stat(remote)
	return err == nil
}

// ReadFile 读远端小文件（.env 用）。
func (c *Client) ReadFile(remote string) (string, error) {
	f, err := c.sftp.Open(remote)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, 1<<16))
	return string(b), err
}

// BuildLinux 交叉编译 linux/amd64 静态二进制，返回产物路径。
//
// 版本号从模块信息注入（-X main.version），这样部署上去的 /v1/health
// 显示的是真实版本，而不是 "dev"——排障时这一点很关键。
func BuildLinux(ctx context.Context, srcDir, out string) error {
	ldflags := "-s -w -X main.version=" + appmeta.Version()
	cmd := exec.CommandContext(ctx, "go", "build",
		"-trimpath", "-ldflags", ldflags, "-o", out, "./cmd/atlas-sync")
	cmd.Dir = srcDir
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("编译失败: %w\n%s", err, tail(string(b), 800))
	}
	return nil
}

// ComposeFile 生成远端 compose：uid 用远端 `id -u`，不写死。
func ComposeFile(uid, gid int) string {
	return fmt.Sprintf(`services:
  atlas-sync:
    build: .
    image: bestuav/atlas-sync:%s
    container_name: atlas-sync
    user: "%d:%d"
    restart: unless-stopped
    env_file: .env
    environment:
      ATLAS_SYNC_DB: /data/atlas-sync.db
      ATLAS_SYNC_LISTEN: :7420
    volumes:
      - ./data:/data
    ports:
      - "17420:7420"
    logging:
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"
`, VersionTag, uid, gid)
}

// Dockerfile 远端镜像定义（只装预编译二进制，构建 3 秒）。
const Dockerfile = `FROM alpine:3.22
RUN adduser -D -u 10001 atlas && mkdir -p /data && chown atlas /data
COPY atlas-sync /usr/local/bin/atlas-sync
USER atlas
WORKDIR /data
EXPOSE 7420
ENTRYPOINT ["/usr/local/bin/atlas-sync"]
`

// Push 一次完整部署：编译 → 建连 → 写配置 → 上传 → 重建容器 → 健康检查。
//
// logf 每完成一步调用一次（由调用方决定怎么展示）；返回的错误已经带上步骤上下文，
// 界面直接显示即可。远端 .env 只在缺失时生成——否则每次部署都会换掉管理员令牌，
// 已配对的桌面与手机全部失效。
func Push(ctx context.Context, t Target, logf func(format string, args ...any)) error {
	// 1) 交叉编译
	logf("① 交叉编译 linux/amd64 …")
	start := time.Now()
	bin := filepath.Join(os.TempDir(), "atlas-sync-linux-amd64")
	if err := BuildLinux(ctx, t.Src, bin); err != nil {
		return err
	}
	defer func() { _ = os.Remove(bin) }()
	size := int64(0)
	if st, err := os.Stat(bin); err == nil {
		size = st.Size()
	}
	logf("   ✓ %.1f MB，用时 %s", float64(size)/(1<<20), time.Since(start).Round(time.Millisecond))

	// 2) SSH
	logf("② 连接服务器 …")
	c, err := Dial(t)
	if err != nil {
		return err
	}
	defer c.Close()

	ids, err := c.Output(ctx, "id -u; id -g")
	if err != nil {
		return fmt.Errorf("读远端 uid/gid 失败: %w", err)
	}
	parts := strings.Fields(ids)
	if len(parts) < 2 {
		return fmt.Errorf("远端 id 输出异常：%q", ids)
	}
	uid, _ := strconv.Atoi(parts[0])
	gid, _ := strconv.Atoi(parts[1])
	logf("   ✓ 远端 uid=%d gid=%d", uid, gid)

	// 3) 远端目录与配置（~ 在这里展开：SFTP 不认它，后面的 Upload/WriteFile 都走 SFTP）
	logf("③ 准备远端目录与配置 …")
	dir := c.Resolve(t.Dir)
	if _, err := c.Run(ctx, "mkdir -p "+shellQuote(dir)+"/data"); err != nil {
		return err
	}
	if err := c.WriteFile(dir+"/Dockerfile", Dockerfile, 0o644); err != nil {
		return err
	}
	if err := c.WriteFile(dir+"/docker-compose.yml", ComposeFile(uid, gid), 0o644); err != nil {
		return err
	}
	if !c.Exists(dir + "/.env") {
		tok, err := c.Output(ctx, "openssl rand -hex 16")
		if err != nil || len(tok) < 16 {
			return fmt.Errorf("生成管理员令牌失败: %v", err)
		}
		if err := c.WriteFile(dir+"/.env", "ATLAS_SYNC_ADMIN_TOKEN="+tok+"\n", 0o600); err != nil {
			return err
		}
		logf("   ✓ 已生成管理员令牌（%s/.env）：%s", dir, tok)
	} else {
		logf("   ✓ 沿用已有 .env（管理员令牌不变）")
	}

	// 4) 上传
	logf("④ 上传二进制 …")
	if err := c.Upload(bin, dir+"/atlas-sync"); err != nil {
		return err
	}
	logf("   ✓ 已上传")

	// 5) 重建容器
	logf("⑤ 重建容器 …")
	out, err := c.Run(ctx, "cd "+shellQuote(dir)+" && docker compose up -d --build 2>&1 | tail -20")
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	logf("%s", strings.TrimSpace(out))

	// 6) 健康检查
	logf("⑥ 健康检查 …")
	health, err := c.Output(ctx, "curl -s --max-time 8 http://127.0.0.1:17420/v1/health")
	if err != nil {
		return fmt.Errorf("容器起来了但健康检查没通: %w", err)
	}
	logf("   %s", health)
	return nil
}

// Restart 只重启容器（改了 compose/环境变量但没换二进制时用）。
func Restart(ctx context.Context, t Target) (string, error) {
	c, err := Dial(t)
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.Run(ctx, "cd "+shellQuote(c.Resolve(t.Dir))+" && docker compose restart atlas-sync 2>&1 | tail -5")
}

// Logs 取最近 n 行容器日志。
func Logs(ctx context.Context, t Target, n int) (string, error) {
	if n <= 0 {
		n = 40
	}
	c, err := Dial(t)
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.Run(ctx, fmt.Sprintf("cd %s && docker logs --tail %d atlas-sync 2>&1",
		shellQuote(c.Resolve(t.Dir)), n))
}

// tail 取文本末尾 n 字节，用于把长输出压进一行错误里。
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
