package main

import (
	"actionscat/pkg/actionscat"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

type AttendanceRecord struct {
	UserID    string    `json:"user_id"`
	Status    string    `json:"status"` // "signed_in" or "signed_out"
	Timestamp time.Time `json:"timestamp"`
}

type AttendanceState struct {
	Records map[string]AttendanceRecord `json:"records"`
}

func main() {
	appCtx := actionscat.GetContext()
	msg := strings.TrimSpace(appCtx.Text)
	userID := appCtx.UserID
	if userID == "" {
		userID = "anonymous"
	}

	var action string
	if strings.Contains(msg, "出勤") {
		action = "出勤"
	} else if strings.Contains(msg, "退勤") {
		action = "退勤"
	} else {
		log.Println("Unrecognized action")
		return
	}

	// 1. Read state from injected environment
	state := AttendanceState{Records: make(map[string]AttendanceRecord)}
	injected := os.Getenv("INJECTED_ATTENDANCE")
	if injected != "" {
		_ = json.Unmarshal([]byte(injected), &state)
	}

	current, exists := state.Records[userID]
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if action == "出勤" {
		if exists && current.Status == "signed_in" {
			_ = actionscat.Reply(ctx, "你已经在勤了！")
			return
		}
		state.Records[userID] = AttendanceRecord{
			UserID:    userID,
			Status:    "signed_in",
			Timestamp: time.Now().UTC(),
		}
	} else {
		if !exists || current.Status == "signed_out" {
			_ = actionscat.Reply(ctx, "你还尚未在勤！")
			return
		}
		state.Records[userID] = AttendanceRecord{
			UserID:    userID,
			Status:    "signed_out",
			Timestamp: time.Now().UTC(),
		}
	}

	// 2. Persist updated state atomically back to ActionsCat Core
	updatedBytes, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		log.Fatalf("marshal state failed: %v", err)
	}

	if err := actionscat.WriteState(ctx, "attendance.json", updatedBytes); err != nil {
		log.Fatalf("write state error: %v", err)
	}

	// 3. Reply to user
	nowStr := time.Now().Format("2006-01-02 15:04:05")
	if action == "出勤" {
		_ = actionscat.Reply(ctx, fmt.Sprintf("【出勤成功！】\n打卡时间: %s", nowStr))
	} else {
		_ = actionscat.Reply(ctx, fmt.Sprintf("【退勤成功！】\n打卡时间: %s", nowStr))
	}

	log.Printf("Maimai attendance updated: user=%s action=%s", userID, action)
}
