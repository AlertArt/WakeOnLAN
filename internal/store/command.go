// 本文件实现 F10 的指令队列：设备关机/重启指令的持久化与状态流转。
// 契约见 docs/F10-command-contract.md；状态取值是契约的一部分，禁止在此新增。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// 指令类型。
// shutdown/restart 会真的关机重启；enable_rdp/disable_rdp 只改系统设置，
// 不涉及倒计时，delay_seconds 对它们没有意义（写入 0）。
const (
	CommandShutdown   = "shutdown"
	CommandRestart    = "restart"
	CommandEnableRDP  = "enable_rdp"
	CommandDisableRDP = "disable_rdp"
)

// NeedsDelay 报告该指令类型是否使用 delay_seconds。
func NeedsDelay(commandType string) bool {
	return commandType == CommandShutdown || commandType == CommandRestart
}

// 指令状态：pending → delivered → done / failed；未取走超时则 expired。
const (
	CommandStatusPending   = "pending"
	CommandStatusDelivered = "delivered"
	CommandStatusDone      = "done"
	CommandStatusFailed    = "failed"
	CommandStatusExpired   = "expired"
)

// CommandTTL 是指令的存活时间：超过这个时间还没被心跳取走就作废。
// 太短会让关机的机器睡一觉起来执行指令，太长又会让误操作迟迟不消失。
const CommandTTL = 5 * time.Minute

// 语义错误。
var (
	ErrCommandPending  = errors.New("已有未完成的指令")
	ErrCommandNotFound = errors.New("指令不存在或已处理")
)

// Command 是一条关机/重启指令。
// DeviceName 只在跨设备查询时填充，单设备查询为空。
type Command struct {
	ID           int64
	DeviceID     int64
	Type         string
	DelaySeconds int
	Force        bool
	Status       string
	Message      string
	CreatedAt    time.Time
	DeliveredAt  time.Time
	FinishedAt   time.Time
	ExpiresAt    time.Time
	DeviceName   string
}

const commandColumns = `id, device_id, type, delay_seconds, force, status, message,
	created_at, delivered_at, finished_at, expires_at`

// CreateCommand 写入一条待下发的指令。
// 同一设备只允许一条 pending/delivered 指令，否则关机和重启会互相覆盖。
func (d *DB) CreateCommand(ctx context.Context, cmd Command) (Command, error) {
	now := time.Now()
	if err := d.expireStale(ctx, now); err != nil {
		return Command{}, err
	}

	busy, err := d.hasOpenCommand(ctx, cmd.DeviceID)
	if err != nil {
		return Command{}, err
	}
	if busy {
		return Command{}, ErrCommandPending
	}

	if cmd.DelaySeconds <= 0 && NeedsDelay(cmd.Type) {
		cmd.DelaySeconds = 60
	}
	cmd.Status = CommandStatusPending
	cmd.Message = ""
	cmd.CreatedAt = now
	cmd.ExpiresAt = now.Add(CommandTTL)

	res, err := d.sql.ExecContext(ctx,
		`INSERT INTO device_command (device_id, type, delay_seconds, force, status, message, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, '', ?, ?)`,
		cmd.DeviceID, cmd.Type, cmd.DelaySeconds, boolToInt(cmd.Force),
		cmd.Status, formatTimeUTC(cmd.CreatedAt), formatTimeUTC(cmd.ExpiresAt),
	)
	if err != nil {
		return Command{}, fmt.Errorf("写入指令失败: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return Command{}, fmt.Errorf("读取指令 ID 失败: %w", err)
	}
	cmd.ID = id
	return cmd, nil
}

// TakePendingCommands 取走该设备所有待下发指令并标记为 delivered。
// 顺带把过期的 pending 指令置为 expired，避免"关机睡三天后开机执行"。
func (d *DB) TakePendingCommands(ctx context.Context, deviceID int64, now time.Time) ([]Command, error) {
	if err := d.expireStale(ctx, now); err != nil {
		return nil, err
	}

	rows, err := d.sql.QueryContext(ctx,
		`SELECT `+commandColumns+` FROM device_command
		  WHERE device_id = ? AND status = ? ORDER BY id`,
		deviceID, CommandStatusPending)
	if err != nil {
		return nil, fmt.Errorf("查询待下发指令失败: %w", err)
	}

	list := make([]Command, 0, 2)
	for rows.Next() {
		cmd, err := scanCommand(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, cmd)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("遍历待下发指令失败: %w", err)
	}
	rows.Close()

	for i := range list {
		if _, err := d.sql.ExecContext(ctx,
			`UPDATE device_command SET status = ?, delivered_at = ? WHERE id = ? AND status = ?`,
			CommandStatusDelivered, formatTimeUTC(now), list[i].ID, CommandStatusPending); err != nil {
			return nil, fmt.Errorf("标记指令已下发失败: %w", err)
		}
		list[i].Status = CommandStatusDelivered
		list[i].DeliveredAt = now
	}

	return list, nil
}

// AckCommand 记录客户端回执。只接受 delivered 状态的指令，
// 重复回执或伪造 ID 都按"不存在"处理。
// 回执成功后返回这条指令的最新快照：调用方需要类型来决定是否顺带更新设备状态
// （enable_rdp/disable_rdp 的结果直接写进 device.rdp_enabled）。
func (d *DB) AckCommand(ctx context.Context, deviceID, commandID int64, ok bool, message string) (Command, error) {
	status := CommandStatusFailed
	if ok {
		status = CommandStatusDone
	}

	res, err := d.sql.ExecContext(ctx,
		`UPDATE device_command SET status = ?, message = ?, finished_at = ?
		  WHERE id = ? AND device_id = ? AND status = ?`,
		status, truncateMessage(message), formatTimeUTC(time.Now()),
		commandID, deviceID, CommandStatusDelivered)
	if err != nil {
		return Command{}, fmt.Errorf("写入指令回执失败: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return Command{}, ErrCommandNotFound
	}
	return d.getCommand(ctx, commandID)
}

// getCommand 按 id 读取单条指令，供回执后回读使用。
func (d *DB) getCommand(ctx context.Context, id int64) (Command, error) {
	row := d.sql.QueryRowContext(ctx, `SELECT `+commandColumns+` FROM device_command WHERE id = ?`, id)
	cmd, err := scanCommand(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Command{}, ErrCommandNotFound
	}
	if err != nil {
		return Command{}, err
	}
	return cmd, nil
}

// ListCommands 返回某设备最近的指令，按 id 倒序。
func (d *DB) ListCommands(ctx context.Context, deviceID int64, limit int) ([]Command, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT `+commandColumns+` FROM device_command
		  WHERE device_id = ? ORDER BY id DESC LIMIT ?`, deviceID, limit)
	if err != nil {
		return nil, fmt.Errorf("查询指令记录失败: %w", err)
	}
	return scanCommands(rows)
}

// ListAllCommands 返回全局最近的指令（带设备名），供审计页与首页提示使用。
func (d *DB) ListAllCommands(ctx context.Context, limit int) ([]Command, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT c.id, c.device_id, c.type, c.delay_seconds, c.force, c.status, c.message,
		        c.created_at, c.delivered_at, c.finished_at, c.expires_at, dv.name
		   FROM device_command c LEFT JOIN device dv ON dv.id = c.device_id
		  ORDER BY c.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("查询指令列表失败: %w", err)
	}

	list := make([]Command, 0, limit)
	for rows.Next() {
		var (
			cmd                          Command
			force                        int
			message, delivered, finished string
			createdAt, expiresAt         string
		)
		if err := rows.Scan(&cmd.ID, &cmd.DeviceID, &cmd.Type, &cmd.DelaySeconds, &force,
			&cmd.Status, &message, &createdAt, &delivered, &finished, &expiresAt, &cmd.DeviceName); err != nil {
			rows.Close()
			return nil, fmt.Errorf("读取指令记录失败: %w", err)
		}
		applyCommandTimes(&cmd, force, message, createdAt, delivered, finished, expiresAt)
		list = append(list, cmd)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("遍历指令列表失败: %w", err)
	}
	rows.Close()
	return list, nil
}

// HasOpenCommand 判断设备是否有未完成指令。
func (d *DB) HasOpenCommand(ctx context.Context, deviceID int64) (bool, error) {
	if err := d.expireStale(ctx, time.Now()); err != nil {
		return false, err
	}
	return d.hasOpenCommand(ctx, deviceID)
}

// ExpireStale 把过期未取走的 pending 指令置为 expired，返回处理条数。
func (d *DB) ExpireStale(ctx context.Context, now time.Time) (int, error) {
	res, err := d.sql.ExecContext(ctx,
		`UPDATE device_command SET status = ?, finished_at = ?, message = ?
		  WHERE status = ? AND expires_at <= ?`,
		CommandStatusExpired, formatTimeUTC(now), "指令过期：设备未在有效期内取走", CommandStatusPending, formatTimeUTC(now))
	if err != nil {
		return 0, fmt.Errorf("清理过期指令失败: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (d *DB) hasOpenCommand(ctx context.Context, deviceID int64) (bool, error) {
	var n int
	if err := d.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM device_command WHERE device_id = ? AND status IN (?, ?)`,
		deviceID, CommandStatusPending, CommandStatusDelivered).Scan(&n); err != nil {
		return false, fmt.Errorf("统计未完成指令失败: %w", err)
	}
	return n > 0, nil
}

func (d *DB) expireStale(ctx context.Context, now time.Time) error {
	_, err := d.ExpireStale(ctx, now)
	return err
}

func scanCommand(rows scanner) (Command, error) {
	var (
		cmd                          Command
		force                        int
		message, delivered, finished string
		createdAt, expiresAt         string
	)
	if err := rows.Scan(&cmd.ID, &cmd.DeviceID, &cmd.Type, &cmd.DelaySeconds, &force,
		&cmd.Status, &message, &createdAt, &delivered, &finished, &expiresAt); err != nil {
		return Command{}, fmt.Errorf("读取指令记录失败: %w", err)
	}
	applyCommandTimes(&cmd, force, message, createdAt, delivered, finished, expiresAt)
	return cmd, nil
}

func scanCommands(rows *sql.Rows) ([]Command, error) {
	defer rows.Close()

	list := make([]Command, 0, 8)
	for rows.Next() {
		cmd, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, cmd)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历指令记录失败: %w", err)
	}
	return list, nil
}

// applyCommandTimes 把库里的字符串时间转回 time.Time，空串表示"尚未发生"。
func applyCommandTimes(cmd *Command, force int, message, createdAt, delivered, finished, expiresAt string) {
	cmd.Force = force != 0
	cmd.Message = message
	cmd.CreatedAt, _ = parseTime(createdAt)
	cmd.DeliveredAt, _ = parseTime(delivered)
	cmd.FinishedAt, _ = parseTime(finished)
	cmd.ExpiresAt, _ = parseTime(expiresAt)
}

// truncateMessage 限制回执长度，避免客户端把整段 stderr 塞进库里。
func truncateMessage(s string) string {
	const max = 300
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// formatTimeUTC 统一按 UTC 存时间。
// 指令要靠字符串比较判断过期，混用本地时区会让比较结果错乱。
func formatTimeUTC(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
