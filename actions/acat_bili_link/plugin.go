package acat_bili_link

import (
	"actionscat/internal/api"
	"actionscat/internal/matcher"
	"fmt"
	"log"
	"regexp"
)

var (
	bvRegex = regexp.MustCompile(`BV1[a-zA-Z0-9]{9}`)
)

// init runs when imported
func init() {
	// reg matcher rule
	rule, err := matcher.NewRule("bilibili-url", "bilibili", "bilibili.com", "b23.tv")
	if err != nil {
		log.Printf("注册规则失败: %v", err)
	}
	err = matcher.GlobalEngine.RegisterRule(rule)
	if err != nil {
		log.Printf("添加规则失败: %v", err)
	}

	// reg executor
	matcher.RegisterExecutor("acat_bili_link", ExecuteBiliLink)
}

func extractBVID(s string) (isB23tv bool, extracted string) {
	if match := b23Regex.FindString(s); match != "" {
		return true, match
	}

	if match := bvRegex.FindString(s); match != "" {
		return false, match
	}

	return false, ""
}

func ExecuteBiliLink(rawMsg string) (any, error) {
	log.Printf("[bili_link] 被触发惹！")

	isShort, bvid := extractBVID(rawMsg)
	if isShort {
		bvid = resolveB23(bvid)
	}

	if bvid == "" {
		return nil, fmt.Errorf("未能提取到有效的 BV 号")
	}

	log.Printf("[bili_link] 正在解析 B 站链接: %s", bvid)

	// 调用 resolve.go 获取真实的视频信息
	detail, err := resolveVideo(bvid)
	if err != nil {
		return nil, fmt.Errorf("获取视频信息失败: %w", err)
	}

	// 将真实的视频信息格式化并返回给前端
	messages := []api.ResponseMessage{
		{
			Type: "text",
			Text: fmt.Sprintf("标题: %s\nUP主: %s\n播放: %d | 点赞: %d\n简介: %s",
				detail.Title,
				detail.UPName,
				detail.View,
				detail.Like,
				detail.Description),
		},
	}

	return messages, nil
}
