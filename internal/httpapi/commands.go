// 本文件实现 F10 的关机/重启指令接口。
//
// 设计要点：
//   - 指令不直接发往目标机，而是入库排队，由心跳回执捎带（目标机仍然零入站端口）；
//   - 只对心跳设备开放：其它校验方式没有下发通道，明确拒绝好过静默排队；
//   - 要求设备当前在线：给离线设备排队等于制造"幽灵关机"。
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"wol/internal/store"
)

// CommandStore 抽象指令队列的持久化能力，由 store.DB 实现。
type CommandStore interface {
	CreateCommand(ctx context.Context, cmd store.Command) (store.Command, error)
	TakePendingCommands(ctx context.Context, deviceID int64, now time.Time) ([]store.Command, error)
	AckCommand(ctx context.Context, deviceID, commandID int64, ok bool, message string) (store.Command, error)
	ListCommands(ctx context.Context, deviceID int64, limit int) ([]store.Command, error)
	ListAllCommands(ctx context.Context, limit int) ([]store.Command, error)
	HasOpenCommand(ctx context.Context, deviceID int64) (bool, error)
}

// 允许的指令类型，与 store 包的常量一一对应。
// shutdown/restart 带倒计时；enable_rdp/disable_rdp 只改系统设置，不需要倒计时。
var commandTypes = map[string]bool{
	store.CommandShutdown:   true,
	store.CommandRestart:    true,
	store.CommandEnableRDP:  true,
	store.CommandDisableRDP: true,
}

const (
	commandDelayMin = 10
	commandDelayMax = 600
	commandLimitDef = 20
	commandLimitMax = 100
)

// commandRequest 是下发请求体。delay_seconds、force 与 allow_blank_password 可省略。
//
// allow_blank_password 是 M2 补充的安全降级开关，只对 enable_rdp 有效：
// 用户显式勾选后才放开「空密码账户禁止网络登录」。默认 false，
// 老客户端不认识这个键，会当作 false 处理，天然向后兼容。
type commandRequest struct {
	Type               string `json:"type"`
	DelaySeconds       *int   `json:"delay_seconds"`
	Force              *bool  `json:"force"`
	AllowBlankPassword *bool  `json:"allow_blank_password"`
}

// commandResponse 是指令的资源形状。时间字段为空串时前端按"未发生"处理。
type commandResponse struct {
	ID           int64  `json:"id"`
	DeviceID     int64  `json:"device_id"`
	Type         string `json:"type"`
	DelaySeconds int    `json:"delay_seconds"`
	Force        bool   `json:"force"`
	Status       string `json:"status"`
	Message      string `json:"message"`
	// AllowBlankPassword 恒存在：前端据此判断这条指令是否做过安全降级。
	AllowBlankPassword bool   `json:"allow_blank_password"`
	CreatedAt          string `json:"created_at"`
	DeliveredAt        string `json:"delivered_at"`
	FinishedAt         string `json:"finished_at"`
	ExpiresAt          string `json:"expires_at"`
	DeviceName         string `json:"device_name,omitempty"`
}

func toCommandResponse(cmd store.Command) commandResponse {
	return commandResponse{
		ID:                 cmd.ID,
		DeviceID:           cmd.DeviceID,
		Type:               cmd.Type,
		DelaySeconds:       cmd.DelaySeconds,
		Force:              cmd.Force,
		Status:             cmd.Status,
		Message:            cmd.Message,
		AllowBlankPassword: cmd.AllowBlankPassword,
		CreatedAt:          formatOptionalTime(cmd.CreatedAt),
		DeliveredAt:        formatOptionalTime(cmd.DeliveredAt),
		FinishedAt:         formatOptionalTime(cmd.FinishedAt),
		ExpiresAt:          formatOptionalTime(cmd.ExpiresAt),
		DeviceName:         cmd.DeviceName,
	}
}

func formatOptionalTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func (s *Server) handleCreateCommand(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	var req commandRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.More() {
		writeError(w, http.StatusBadRequest, "invalid_json", "请求体应为 {\"type\":\"shutdown\",\"delay_seconds\":60,\"force\":false}")
		return
	}

	if !commandTypes[req.Type] {
		writeError(w, http.StatusBadRequest, "invalid_command",
			"指令类型只支持 shutdown、restart、enable_rdp、disable_rdp")
		return
	}

	// 只有关机/重启用倒计时。RDP 开关没有"几秒后执行"的语义，
	// 传了也忽略并写 0，免得日志里出现一个没人使用的延时。
	delay := 0
	force := false
	allowBlank := false
	if store.NeedsDelay(req.Type) {
		delay = 60
		if req.DelaySeconds != nil {
			delay = *req.DelaySeconds
		}
		if delay < commandDelayMin || delay > commandDelayMax {
			writeError(w, http.StatusBadRequest, "invalid_command", "延时必须在 10–600 秒之间")
			return
		}
		force = req.Force != nil && *req.Force
	}

	// 空密码放行只对"启用远程桌面"有意义，其它类型带了也拒绝：
	// 静默忽略会让调用方以为自己授权生效了，而这个开关是安全降级，不能含糊。
	if req.AllowBlankPassword != nil && *req.AllowBlankPassword {
		if req.Type != store.CommandEnableRDP {
			writeError(w, http.StatusBadRequest, "invalid_command",
				"allow_blank_password 只能与 enable_rdp 一起使用")
			return
		}
		allowBlank = true
	}

	dv, err := s.devices.GetDevice(r.Context(), id)
	if err != nil {
		s.fail(w, "查询待关机设备", err)
		return
	}

	// 只有心跳设备有下发通道；其余类型下发只会永远挂着。
	if dv.ProbeType != probeHeartbeat {
		writeError(w, http.StatusBadRequest, "not_heartbeat",
			"该设备不是心跳校验方式，无法下发指令；请先把校验方式改为「心跳上报」并在目标机运行客户端")
		return
	}

	online, err := s.deviceOnline(r.Context(), id)
	if err != nil {
		s.logger.Error("查询设备在线状态失败", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "服务内部错误")
		return
	}
	if !online {
		writeError(w, http.StatusConflict, "device_offline",
			"设备当前不在线（最近没有心跳），指令无法送达；请等设备上线后再操作")
		return
	}

	cmd, err := s.commands.CreateCommand(r.Context(), store.Command{
		DeviceID:           id,
		Type:               req.Type,
		DelaySeconds:       delay,
		Force:              force,
		AllowBlankPassword: allowBlank,
	})
	switch {
	case errors.Is(err, store.ErrCommandPending):
		writeError(w, http.StatusConflict, "command_pending", "该设备已有未完成的指令，请等它执行完")
		return
	case err != nil:
		s.logger.Error("写入指令失败", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "写入指令失败")
		return
	}

	s.logger.Info("已下发指令", "device_id", id, "command", cmd.Type, "delay", delay,
		"allow_blank_password", allowBlank, "command_id", cmd.ID)
	writeJSON(w, http.StatusCreated, toCommandResponse(cmd))
}

func (s *Server) handleListDeviceCommands(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	if _, err := s.devices.GetDevice(r.Context(), id); err != nil {
		s.fail(w, "查询设备指令", err)
		return
	}

	list, err := s.commands.ListCommands(r.Context(), id, parseLimit(r))
	if err != nil {
		s.logger.Error("查询指令记录失败", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "服务内部错误")
		return
	}
	writeJSON(w, http.StatusOK, toCommandResponses(list))
}

func (s *Server) handleListCommands(w http.ResponseWriter, r *http.Request) {
	list, err := s.commands.ListAllCommands(r.Context(), parseLimit(r))
	if err != nil {
		s.logger.Error("查询指令列表失败", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "服务内部错误")
		return
	}
	writeJSON(w, http.StatusOK, toCommandResponses(list))
}

func toCommandResponses(list []store.Command) []commandResponse {
	out := make([]commandResponse, 0, len(list))
	for _, cmd := range list {
		out = append(out, toCommandResponse(cmd))
	}
	return out
}

// deviceOnline 用与 /api/status 相同的推算规则判断在线：
// 心跳设备的结论只看"最近一次心跳距今多久"，不读 probe_status 的落库值。
func (s *Server) deviceOnline(ctx context.Context, deviceID int64) (bool, error) {
	list, err := s.statuses.ListProbeStatus(ctx)
	if err != nil {
		return false, err
	}
	for _, item := range list {
		if item.DeviceID != deviceID {
			continue
		}
		status, _ := heartbeatStatus(item.CheckedAt, time.Now())
		return status == store.StatusOnline, nil
	}
	return false, store.ErrNotFound
}

// parseLimit 解析 ?limit=，非法或越界时回落到默认值而不是报错：
// 查询类参数不值得让整个请求失败。
func parseLimit(r *http.Request) int {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return commandLimitDef
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return commandLimitDef
	}
	if n > commandLimitMax {
		return commandLimitMax
	}
	return n
}
