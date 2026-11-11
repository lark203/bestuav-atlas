// Package syncclient 是同步的客户端：HTTP 调用 + 一次完整的「推上去、拉回来」。
//
// 桌面端与手机端用的是同一套协议，但只有桌面端用这个包（手机端的 HTTP 在 Dart 里）。
// 放在 atlas 仓而不是共享库，是因为它绑定 store 的语义：合并、水位、outbox 都在那边。
package syncclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"bestuav-atlas/internal/store"
	"bestuav-atlas/internal/syncwire"
)

// Client 一个服务端实例的客户端。字段不可变：每个请求从同一份配置出发，
// 免得同步线程读到用户正在改的设置。
type Client struct {
	BaseURL  string
	DeviceID string // 只用于服务端日志与「哪台设备推的」，服务端以令牌为准
	Token    string
	HTTP     *http.Client
}

// New 构造客户端；baseURL 末尾的斜杠会被去掉。
func New(baseURL, deviceID, token string) *Client {
	return &Client{
		BaseURL:  strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		DeviceID: deviceID,
		Token:    token,
		HTTP:     &http.Client{Timeout: 30 * time.Second},
	}
}

// Health 服务端概况（版本、水位、设备数）。
type Health struct {
	Version    string `json:"version"`
	Schema     string `json:"schema"`
	Protocol   int    `json:"protocol"`
	Devices    int64  `json:"devices"`
	Ops        int64  `json:"ops"`
	Seq        int64  `json:"seq"`
	ServerTime string `json:"server_time"`
}

// Health 探活，同时用于设置页的「测试连接」。
func (c *Client) Health(ctx context.Context) (Health, error) {
	var h Health
	err := c.do(ctx, http.MethodGet, "/v1/health", nil, &h, "")
	return h, err
}

// Pair 用一次性配对码换设备令牌（手机端流程；桌面端也可以用同一条）。
func (c *Client) Pair(ctx context.Context, code, deviceName string) (syncwire.PairResponse, error) {
	var out syncwire.PairResponse
	err := c.do(ctx, http.MethodPost, "/v1/pair",
		syncwire.PairRequest{Code: code, DeviceName: deviceName}, &out, "")
	return out, err
}

// AdminCreateDevice 直接登记一台设备并拿令牌（桌面端首次连接用）。
func (c *Client) AdminCreateDevice(ctx context.Context, adminToken, deviceName string) (syncwire.PairResponse, error) {
	var out syncwire.PairResponse
	err := c.do(ctx, http.MethodPost, "/v1/admin/devices",
		syncwire.PairRequest{DeviceName: deviceName}, &out, adminToken)
	return out, err
}

// AdminPairCode 生成一次性配对码（桌面端「添加手机」用）。
func (c *Client) AdminPairCode(ctx context.Context, adminToken string) (string, time.Time, error) {
	var out struct {
		Code      string `json:"code"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/admin/pair-codes", struct{}{}, &out, adminToken); err != nil {
		return "", time.Time{}, err
	}
	exp, _ := time.Parse(time.RFC3339Nano, out.ExpiresAt)
	return out.Code, exp, nil
}

// Push 上传一批变更。
func (c *Client) Push(ctx context.Context, ops []syncwire.Op) (syncwire.PushResponse, error) {
	var out syncwire.PushResponse
	err := c.do(ctx, http.MethodPost, "/v1/changes",
		syncwire.PushRequest{DeviceID: c.DeviceID, Ops: ops}, &out, "")
	return out, err
}

// Pull 拉取 since 之后的变更。
func (c *Client) Pull(ctx context.Context, since int64, limit int) (syncwire.PullResponse, error) {
	path := fmt.Sprintf("/v1/changes?since=%d&limit=%d", since, limit)
	var out syncwire.PullResponse
	err := c.do(ctx, http.MethodGet, path, nil, &out, "")
	return out, err
}

// Snapshot 拉取全量当前状态（首次配对，或本地库被重建时）。
func (c *Client) Snapshot(ctx context.Context) (syncwire.SnapshotResponse, error) {
	var out syncwire.SnapshotResponse
	err := c.do(ctx, http.MethodGet, "/v1/snapshot", nil, &out, "")
	return out, err
}

// do 是唯一发出请求的地方：鉴权头、错误体解析、超时都收在这里。
func (c *Client) do(ctx context.Context, method, path string, in, out any, adminToken string) error {
	if c.BaseURL == "" {
		return errors.New("syncclient: 未配置同步服务地址")
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("syncclient: 编码请求失败: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return fmt.Errorf("syncclient: 构造请求失败: %w", err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if adminToken != "" {
		req.Header.Set("X-Admin-Token", adminToken)
	}

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("syncclient: 请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 服务端的错误体是 {"error": "..."}；不是 JSON 时退回状态码，至少能看懂发生了什么。
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return fmt.Errorf("syncclient: %s（HTTP %d）", e.Error, resp.StatusCode)
		}
		return fmt.Errorf("syncclient: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("syncclient: 解析响应失败: %w", err)
	}
	return nil
}

// RoundResult 一次同步的结果（给界面与日志用）。
type RoundResult struct {
	Pushed  int           // 上传条数
	Pulled  int           // 拉回条数（含自己发出去的回环）
	Applied int           // 真正应用到本地的条数
	Skipped int           // 被跳过（本地有未发送修改、未知实体等）
	Seq     int64         // 同步后的服务端水位
	Elapsed time.Duration // 用时
}

// PushPull 跑完整的一轮同步：**先按水位把别人的变更拉回来，再推自己的**。
//
// 顺序是收敛性的关键（M4 用 TestSameFieldConcurrentConverges 钉住的）：
//
//   - 先拉后推：拉回来的远端补丁遇到「本地有未发送修改」的字段会被跳过（store 的
//     pendingFields 保护），所以本地没发出去的意见不会被对方的旧值盖掉；推上去之后
//     对方再拉，看到的最后值就是这边——**后推者赢，且两边一致**。
//   - 反过来（先推后拉）会出现：B 推完自己的标题、再拉到 A 更早的标题，而没有本地
//     待发送修改可以挡住它，于是 B 被改回 A 的旧值；A 又已经被 B 覆盖——两边永久分叉。
//
// 代价：推上去的变更下一轮会被自己拉回来一次（按 op_id 跳过）。换来的是收敛性，
// 这点流量不值得优化。
func PushPull(ctx context.Context, core *store.Store, c *Client, limit int) (RoundResult, error) {
	start := time.Now()
	var res RoundResult
	if limit <= 0 || limit > 1000 {
		limit = 500
	}

	// 1) 先拉：把远端已经发生的变更合进来。
	since, err := lastSeq(core)
	if err != nil {
		return res, err
	}
	// 分页拉取：一次最多 limit 条，直到服务端没有更多。
	// 上限 100 页是防「服务端一直回同一批」这类病态情况把客户端拖死。
	for page := 0; page < 100; page++ {
		pulled, err := c.Pull(ctx, since, limit)
		if err != nil {
			return res, fmt.Errorf("拉取变更失败: %w", err)
		}
		if len(pulled.Ops) == 0 {
			if pulled.Next > since {
				since = pulled.Next
			}
			break
		}
		applied, skipped, err := core.ApplyOps(pulled.Ops)
		if err != nil {
			return res, err
		}
		if err := core.MarkOpsSent(nil, pulled.Next); err != nil {
			return res, err
		}
		res.Pulled += len(pulled.Ops)
		res.Applied += applied
		res.Skipped += len(skipped)
		if pulled.Next <= since {
			break // 服务端没前进，继续拉只会拿到同一批
		}
		since = pulled.Next
		if len(pulled.Ops) < limit {
			break
		}
	}

	// 2) 再推：本地 outbox 里所有未发送的变更（含刚刚被保护下来的本地修改）。
	ops, err := core.PendingOps()
	if err != nil {
		return res, err
	}
	if len(ops) > 0 {
		if _, err := c.Push(ctx, ops); err != nil {
			return res, fmt.Errorf("上传变更失败: %w", err)
		}
		ids := make([]string, 0, len(ops))
		for _, op := range ops {
			ids = append(ids, op.OpID)
		}
		// 只标记已发送，**不推进水位**：服务端返回的 seq 是全局水位，可能已经包含
		// 别的设备在这中间推的变更。拿它当 last_seq 会直接跳过那几条，而且再也拉不回来。
		// 水位只由上面的「拉取并应用」推进——自己发出去的 op 会在下一轮拉取时按 op_id 跳过。
		if err := core.MarkOpsSent(ids, 0); err != nil {
			return res, err
		}
		res.Pushed = len(ops)
	}

	res.Seq = since
	res.Elapsed = time.Since(start)
	return res, nil
}

// lastSeq 读本地水位；空/坏值都当 0（从最早开始拉，合并是幂等的）。
func lastSeq(core *store.Store) (int64, error) {
	v, err := core.SyncState("last_seq")
	if err != nil || v == "" {
		return 0, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, nil
	}
	return n, nil
}
