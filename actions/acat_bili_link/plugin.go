package acat_bili_link

import (
	"actionscat/internal/matcher"
	"fmt"
	"log"
)

// init runs when imported
func init() {
	// reg matcher rule
	ruleSpec := matcher.NewURLRule("bilibili-url", "bilibili", "bilibili.com", "b23.tv")
	rule, _ := matcher.NewRegexRule(ruleSpec)
	err := matcher.GlobalEngine.RegisterRule(rule)
	if err != nil {
		log.Printf("注册规则失败: %v", err)
	}

	// reg executor
	matcher.RegisterExecutor("bilibili", ExecuteBiliLink)
}

func ExecuteBiliLink(rawMsg string) (any, error) {
	// [wip] detailed action logic
	fmt.Println("正在解析 B 站链接:", rawMsg)

	// 模拟返回
	return map[string]string{
		"title": "小米自研玄戒O1芯片深度评测",
		"up":    "极客湾",
	}, nil
}
