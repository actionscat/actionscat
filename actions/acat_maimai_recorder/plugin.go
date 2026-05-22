package acat_maimai_recorder

import (
	"actionscat/internal/api"
	"actionscat/internal/matcher"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// AttendanceRecord 记录出勤状态
type AttendanceRecord struct {
	QQ        string
	Status    string // "signed_in" 或 "signed_out"
	Timestamp time.Time
}

// AttendanceManager 管理出勤记录
type AttendanceManager struct {
	records map[string]AttendanceRecord
	mu      sync.RWMutex
}

var manager = &AttendanceManager{
	records: make(map[string]AttendanceRecord),
}

func init() {
	// 注册规则
	rule, err := matcher.NewRule("attendance-rule", "acat_maimai_recorder", "出勤", "退勤")
	if err != nil {
		log.Printf("[acat_maimai_recorder] 注册规则失败: %v", err)
	}
	err = matcher.GlobalEngine.RegisterRule(rule)
	if err != nil {
		log.Printf("[acat_maimai_recorder] 添加规则失败: %v", err)
	}

	// 注册执行器
	matcher.RegisterExecutor("acat_maimai_recorder", HandleAttendance)
}

// HandleAttendance 处理出勤和退勤
func HandleAttendance(ctx matcher.ExecutionContext) (any, error) {
	log.Printf("[acat_maimai_recorder] 处理出勤/退勤请求, 用户: %s, 消息: %s", ctx.SenderQQ, ctx.RawMsg)

	msg := strings.TrimSpace(ctx.RawMsg)

	var status string
	var action string

	if strings.Contains(msg, "出勤") {
		status = "signed_in"
		action = "出勤"
	} else if strings.Contains(msg, "退勤") {
		status = "signed_out"
		action = "退勤"
	} else {
		return nil, fmt.Errorf("无法识别的操作")
	}

	// 检查当前状态
	currentRecord, exists := GetAttendanceStatus(ctx.SenderQQ)

	// 如果要出勤，检查是否已经在勤
	if action == "出勤" {
		if exists && currentRecord.Status == "signed_in" {
			return []api.ResponseMessage{
				{Type: "text", Text: "你已经在勤了！"},
			}, nil
		}
	}

	// 如果要退勤，检查是否已经不在勤
	if action == "退勤" {
		if !exists || currentRecord.Status == "signed_out" {
			return []api.ResponseMessage{
				{Type: "text", Text: "你还尚未在勤！"},
			}, nil
		}
	}

	// 记录出勤状态
	record := AttendanceRecord{
		QQ:        ctx.SenderQQ,
		Status:    status,
		Timestamp: time.Now(),
	}

	manager.mu.Lock()
	manager.records[ctx.SenderQQ] = record
	manager.mu.Unlock()

	var response string

	if action == "出勤" {
		response = fmt.Sprintf("【出勤成功！】\n->g2街区盐城聚龙湖金鹰店\n时间: %s", record.Timestamp.Format("2006-01-02 15:04:05"))
	} else {
		response = fmt.Sprintf("【退勤成功！】\n时间: %s", record.Timestamp.Format("2006-01-02 15:04:05"))
	}
	return []api.ResponseMessage{
		{Type: "text", Text: response},
	}, nil
}

// GetAttendanceStatus 获取出勤状态
func GetAttendanceStatus(qq string) (AttendanceRecord, bool) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	record, exists := manager.records[qq]
	return record, exists
}

// GetAllRecords 获取所有记录
func GetAllRecords() map[string]AttendanceRecord {
	manager.mu.RLock()
	defer manager.mu.RUnlock()

	result := make(map[string]AttendanceRecord)
	for k, v := range manager.records {
		result[k] = v
	}
	return result
}
