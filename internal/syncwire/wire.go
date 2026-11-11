// Package syncwire 定义「桌面 / 手机 / 同步服务」三方共用的传输结构。
//
// 这里只有数据形状，没有逻辑：解析、合并、校验都在各自的包里做。
// 单独成包是为了让 mobile（gomobile 绑定）与 cmd/atlas-sync 引用同一份定义，
// 避免出现「客户端叫 project_uid、服务端叫 projectId」这种只在联调时才发现的漂移。
//
// 命名约定：
//   - JSON 键一律 snake_case，与数据库列名保持一致——多一层驼峰映射就多一层出错机会；
//   - 时间一律 RFC3339Nano 的 UTC 字符串，空串表示零值（与 store 的 timeText 对称）；
//   - patch 的键是「列名」，唯一例外是 project_uid：库里存的是 project_id（本地自增），
//     跨设备没有意义，所以同步一律走 uid。
package syncwire

// 实体类型。
const (
	EntityTodo    = "todo"
	EntityProject = "project"
)

// 变更种类。
const (
	KindCreate = "create" // patch = 整行
	KindPatch  = "patch"  // patch = 只含改动字段
	KindDelete = "delete" // patch 为空
)

// ProtocolVersion 协议版本。不兼容变更时 +1，服务端据此拒绝老客户端。
const ProtocolVersion = 1

// Op 一条变更。op_id 由产生方生成（ULID），服务端按 (device_id, op_id) 去重。
type Op struct {
	OpID      string         `json:"op_id"`
	Entity    string         `json:"entity"`
	UID       string         `json:"uid"`
	Kind      string         `json:"kind"`
	Patch     map[string]any `json:"patch,omitempty"`
	CreatedAt string         `json:"created_at"`
	// Seq 是服务端分配的水位，本地产生时为 0；拉取回来时才有值。
	Seq int64 `json:"seq,omitempty"`
	// DeviceID 由服务端回填，客户端拉取时据此跳过自己发过的 op。
	DeviceID string `json:"device_id,omitempty"`
}

// Todo 一条待办在网线上的样子。
type Todo struct {
	UID        string `json:"uid"`
	Title      string `json:"title"`
	Note       string `json:"note"`
	ProjectUID string `json:"project_uid"`
	Priority   int    `json:"priority"`
	Important  bool   `json:"important"`
	Urgent     bool   `json:"urgent"`
	Status     string `json:"status"`
	StartAt    string `json:"start_at"`
	DueAt      string `json:"due_at"`
	DoneAt     string `json:"done_at"`
	NotifiedAt string `json:"notified_at"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// Project 一个项目在网线上的样子。
type Project struct {
	UID       string `json:"uid"`
	Name      string `json:"name"`
	Desc      string `json:"desc"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// PushRequest 客户端推送的一批变更。
type PushRequest struct {
	DeviceID string `json:"device_id"`
	Ops      []Op   `json:"ops"`
}

// PushResponse 推送结果：accepted 为入账条数，seq 为本次写入后的服务端水位。
type PushResponse struct {
	Accepted int   `json:"accepted"`
	Seq      int64 `json:"seq"`
}

// PullResponse 拉取结果。
type PullResponse struct {
	Ops        []Op   `json:"ops"`
	Next       int64  `json:"next"` // 下次 since 用这个值
	ServerTime string `json:"server_time"`
}

// SnapshotResponse 首次配对的快照：当前合并后的全部实体。
type SnapshotResponse struct {
	Todos    []Todo    `json:"todos"`
	Projects []Project `json:"projects"`
	Seq      int64     `json:"seq"`
}

// PairRequest 用一次性配对码换设备令牌。
type PairRequest struct {
	Code       string `json:"code"`
	DeviceName string `json:"device_name"`
}

// PairResponse 配对结果。token 只在这里出现一次，之后客户端自己保存。
type PairResponse struct {
	DeviceID string `json:"device_id"`
	Token    string `json:"token"`
}
