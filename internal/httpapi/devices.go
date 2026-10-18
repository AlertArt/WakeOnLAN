// 本文件实现 F1 设备管理的四个接口。
// 校验全部前置：先解析、再校验、最后写库，任一步失败都不产生副作用。
// 响应形状严格按 docs/E1-contract.md，禁止追加字段。
package httpapi

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"wol/internal/store"
)

// deviceResponse 是契约中的资源模型，字段集合即白名单，共 9 个键。
type deviceResponse struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	MAC           string `json:"mac"`
	BroadcastAddr string `json:"broadcast_addr"`
	BroadcastPort int    `json:"broadcast_port"`
	ProbeType     string `json:"probe_type"`
	ProbeValue    string `json:"probe_value"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

func toDeviceResponse(dv store.Device) deviceResponse {
	return deviceResponse{
		ID:            dv.ID,
		Name:          dv.Name,
		MAC:           dv.MAC,
		BroadcastAddr: dv.BroadcastAddr,
		BroadcastPort: dv.BroadcastPort,
		ProbeType:     dv.ProbeType,
		ProbeValue:    dv.ProbeValue,
		CreatedAt:     dv.CreatedAt.Format(time.RFC3339),
		UpdatedAt:     dv.UpdatedAt.Format(time.RFC3339),
	}
}

// deviceRequest 是写入用的请求体。id 与时间戳由服务端生成，此结构体中无对应字段，
// 客户端多传会被 DisallowUnknownFields 拒绝。
type deviceRequest struct {
	Name          string `json:"name"`
	MAC           string `json:"mac"`
	BroadcastAddr string `json:"broadcast_addr"`
	BroadcastPort *int   `json:"broadcast_port"`
	ProbeType     string `json:"probe_type"`
	ProbeValue    string `json:"probe_value"`
}

const (
	probeNone = "none"
	probePing = "ping"
	probeTCP  = "tcp"

	defaultBroadcastPort = 9
	maxNameLen           = 32
)

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	list, err := s.devices.ListDevices(r.Context())
	if err != nil {
		s.fail(w, "查询设备列表", err)
		return
	}

	// 显式构造切片，确保无数据时输出 [] 而不是 null。
	out := make([]deviceResponse, 0, len(list))
	for _, dv := range list {
		out = append(out, toDeviceResponse(dv))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateDevice(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeDeviceRequest(w, r)
	if !ok {
		return
	}

	dv, ok := toStoreDevice(w, req)
	if !ok {
		return
	}

	created, err := s.devices.CreateDevice(r.Context(), dv)
	if err != nil {
		s.fail(w, "新增设备", err)
		return
	}
	writeJSON(w, http.StatusCreated, toDeviceResponse(created))
}

func (s *Server) handleUpdateDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	req, ok := decodeDeviceRequest(w, r)
	if !ok {
		return
	}

	dv, ok := toStoreDevice(w, req)
	if !ok {
		return
	}
	dv.ID = id

	updated, err := s.devices.UpdateDevice(r.Context(), dv)
	if err != nil {
		s.fail(w, "修改设备", err)
		return
	}
	writeJSON(w, http.StatusOK, toDeviceResponse(updated))
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	if err := s.devices.DeleteDevice(r.Context(), id); err != nil {
		s.fail(w, "删除设备", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fail 把存储层语义错误映射为契约中的状态码；未识别错误一律 500，且不回显内部细节。
func (s *Server) fail(w http.ResponseWriter, action string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "设备不存在")
	case errors.Is(err, store.ErrDuplicateMAC):
		writeError(w, http.StatusConflict, "duplicate_mac", "该 MAC 已被其他设备使用")
	case errors.Is(err, store.ErrLimitExceeded):
		writeError(w, http.StatusConflict, "limit_exceeded", "设备数已达上限，请先删除不再使用的设备")
	default:
		s.logger.Error(action+"失败", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "服务内部错误")
	}
}

func parseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_id", "设备 ID 必须为正整数")
		return 0, false
	}
	return id, true
}

// decodeDeviceRequest 解析请求体，拒绝未知字段与多余的 JSON 值。
func decodeDeviceRequest(w http.ResponseWriter, r *http.Request) (deviceRequest, bool) {
	var req deviceRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "请求体不是合法 JSON，或包含契约之外的字段")
		return deviceRequest{}, false
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, "invalid_json", "请求体只能包含一个 JSON 对象")
		return deviceRequest{}, false
	}
	return req, true
}

// toStoreDevice 完成全部字段校验与归一化，任一项不通过即写出 400 并返回 false。
func toStoreDevice(w http.ResponseWriter, req deviceRequest) (store.Device, bool) {
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > maxNameLen {
		writeError(w, http.StatusBadRequest, "invalid_name", "名称不能为空且不超过 32 个字符")
		return store.Device{}, false
	}

	mac, ok := normalizeMAC(req.MAC)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_mac", "MAC 格式应为 AA:BB:CC:DD:EE:FF、AA-BB-CC-DD-EE-FF 或 12 位十六进制")
		return store.Device{}, false
	}

	addr := strings.TrimSpace(req.BroadcastAddr)
	if !isIPv4(addr) {
		writeError(w, http.StatusBadRequest, "invalid_broadcast_addr", "广播地址必须是合法的 IPv4 地址，例如 192.168.1.255")
		return store.Device{}, false
	}

	port := defaultBroadcastPort
	if req.BroadcastPort != nil {
		port = *req.BroadcastPort
	}
	if port < 1 || port > 65535 {
		writeError(w, http.StatusBadRequest, "invalid_broadcast_port", "广播端口必须在 1–65535 之间")
		return store.Device{}, false
	}

	probeType := strings.TrimSpace(req.ProbeType)
	if probeType == "" {
		probeType = probeNone
	}
	probeValue := strings.TrimSpace(req.ProbeValue)

	switch probeType {
	case probeNone:
		// 未配置校验目标时固定为空串，不接受残留值。
		probeValue = ""
	case probePing:
		if !isIPv4(probeValue) {
			writeError(w, http.StatusBadRequest, "invalid_probe", "校验方式为 ping 时，目标必须是合法 IPv4 地址")
			return store.Device{}, false
		}
	case probeTCP:
		if _, _, err := net.SplitHostPort(probeValue); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_probe", "校验方式为 tcp 时，目标应为主机:端口，例如 192.168.1.10:22")
			return store.Device{}, false
		}
	default:
		writeError(w, http.StatusBadRequest, "invalid_probe", "校验方式只能是 none、ping 或 tcp")
		return store.Device{}, false
	}

	return store.Device{
		Name:          name,
		MAC:           mac,
		BroadcastAddr: addr,
		BroadcastPort: port,
		ProbeType:     probeType,
		ProbeValue:    probeValue,
	}, true
}

// normalizeMAC 接受三种写法，统一输出大写冒号格式；分隔符混用视为非法。
func normalizeMAC(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}

	var hex string
	switch {
	case strings.Contains(s, ":") && strings.Contains(s, "-"):
		return "", false // 分隔符混用
	case strings.Contains(s, ":") || strings.Contains(s, "-"):
		sep := ":"
		if strings.Contains(s, "-") {
			sep = "-"
		}
		parts := strings.Split(s, sep)
		if len(parts) != 6 {
			return "", false
		}
		for _, p := range parts {
			if len(p) != 2 {
				return "", false
			}
			hex += p
		}
	default:
		hex = s
	}

	if len(hex) != 12 {
		return "", false
	}
	for _, c := range hex {
		if !isHexDigit(c) {
			return "", false
		}
	}

	hex = strings.ToUpper(hex)
	var b strings.Builder
	b.Grow(17)
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(hex[i : i+2])
	}
	return b.String(), true
}

func isHexDigit(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// isIPv4 只接受点分十进制，拒绝 IPv6 与主机名。
func isIPv4(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil && strings.Count(s, ".") == 3
}
