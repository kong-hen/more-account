package wakeup

import (
	"testing"
	"time"

	"more-account/internal/model"
)

func TestParseStreamSSEResponse(t *testing.T) {
	sse := "" +
		"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"thinking...\",\"thought\":true}]}}],\"responseId\":\"resp-1\"}}\n\n" +
		"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hello\"}]}}],\"usageMetadata\":{\"promptTokenCount\":12,\"candidatesTokenCount\":3,\"totalTokenCount\":15}},\"traceId\":\"tr-1\"}\n\n" +
		"data: [DONE]\n"

	parsed, err := ParseStreamResult(sse)
	if err != nil {
		t.Fatalf("解析 SSE 唤醒响应失败: %v", err)
	}
	if parsed.Reply != "Hello" {
		t.Fatalf("期望过滤思考段并得到 Hello，实际 %q", parsed.Reply)
	}
	if parsed.PromptTokens != 12 || parsed.CompletionTokens != 3 || parsed.TotalTokens != 15 {
		t.Fatalf("Token 统计不匹配: %+v", parsed)
	}
}

func TestShouldTriggerScheduleModes(t *testing.T) {
	// 2026-11-21 是周六 (time.Saturday == 6)
	now := time.Date(2026, 11, 21, 9, 30, 0, 0, time.Local)

	// 1. 每日定点命中
	dailyTask := model.WakeupTask{
		ID:      "t-daily",
		Enabled: true,
		Schedule: model.WakeupSchedule{
			RepeatMode: "daily",
			DailyTimes: []string{"09:30", "18:00"},
		},
	}
	if !ShouldTriggerTask(dailyTask, now, 0) {
		t.Fatal("每日 09:30 应当触发")
	}
	// 同一分钟内已执行过则防重
	if ShouldTriggerTask(dailyTask, now, now.UnixMilli()) {
		t.Fatal("同分钟内不应重复触发")
	}

	// 2. 每周定点
	weeklyTask := model.WakeupTask{
		ID:      "t-weekly",
		Enabled: true,
		Schedule: model.WakeupSchedule{
			RepeatMode:  "weekly",
			WeeklyDays:  []int{int(time.Saturday)},
			WeeklyTimes: []string{"09:30"},
		},
	}
	if !ShouldTriggerTask(weeklyTask, now, 0) {
		t.Fatal("每周六 09:30 应当触发")
	}

	// 3. Cron 表达式
	cronTask := model.WakeupTask{
		ID:      "t-cron",
		Enabled: true,
		Schedule: model.WakeupSchedule{
			RepeatMode: "crontab",
			Crontab:    "30 9 * * 6",
		},
	}
	if !ShouldTriggerTask(cronTask, now, 0) {
		t.Fatal("Cron '30 9 * * 6' 应当命中周六 09:30")
	}

	// 4. 固定间隔 + 时间窗
	intervalTask := model.WakeupTask{
		ID:      "t-interval",
		Enabled: true,
		Schedule: model.WakeupSchedule{
			RepeatMode:        "interval",
			IntervalHours:     4,
			IntervalStartTime: "08:00",
			IntervalEndTime:   "22:00",
		},
	}
	fiveHoursAgo := now.Add(-5 * time.Hour).UnixMilli()
	if !ShouldTriggerTask(intervalTask, now, fiveHoursAgo) {
		t.Fatal("距上次执行超过 4 小时且在时间窗内应当触发")
	}
	oneHourAgo := now.Add(-1 * time.Hour).UnixMilli()
	if ShouldTriggerTask(intervalTask, now, oneHourAgo) {
		t.Fatal("距上次执行不足 4 小时不应触发")
	}
}
