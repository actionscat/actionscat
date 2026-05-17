package acat_bili_link

import (
	"actionscat/internal/matcher"
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
	matcher.RegisterExecutor("bilibili", ExecuteBiliLink)
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

	// [wip] detailed action logic
	isShort, bvid := extractBVID(rawMsg)
	if isShort {
		bvid = resolveB23(bvid)
	}
	log.Printf("[bili_link] 正在解析 B 站链接: %s", bvid)

	// 模拟返回
	return map[string]string{
		"title": "小米自研玄戒O1芯片深度评测",
		"up":    "极客湾",
	}, nil
}
