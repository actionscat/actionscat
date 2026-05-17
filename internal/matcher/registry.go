package matcher

import (
	"fmt"
	_ "fmt"
	"regexp"
	"strings"
)

// 1. 全局的匹配引擎
var GlobalEngine = &SimpleEngine{
	rules: []*Rule{},
}

// 2. 插件执行器函数签名：接收原始请求，返回处理结果
type ExecutorFunc func(rawMsg string) (any, error)

// 3. 全局的执行器注册表：通过 ActionName 映射到具体的执行函数
var executorRegistry = make(map[string]ExecutorFunc)

// 注册执行器
func RegisterExecutor(actionName string, exec ExecutorFunc) {
	executorRegistry[actionName] = exec
}

// 获取执行器
func GetExecutor(actionName string) (ExecutorFunc, bool) {
	exec, exists := executorRegistry[actionName]
	return exec, exists
}

type MatchResult struct {
	ActionName string
	Data       any
}

type SimpleEngine struct {
	rules []*Rule
}

func (e *SimpleEngine) RegisterRule(rule *Rule) error {
	if rule == nil {
		return nil
	}
	e.rules = append(e.rules, rule)
	return nil
}

func NewRule(name string, actionName string, domains ...string) (*Rule, error) {
	if len(domains) == 0 {
		return nil, fmt.Errorf("domains list cannot be empty for rule %s", name)
	}

	// 1. 自动遍历并转义域名中的特殊字符 (比如把 "." 转义成 "\.")
	escapedDomains := make([]string, 0, len(domains))
	for _, domain := range domains {
		escapedDomains = append(escapedDomains, regexp.QuoteMeta(domain))
	}

	// 2. 用 "|" 拼接多个域名，形成正则的 OR 逻辑
	domainPattern := strings.Join(escapedDomains, "|")

	// 3. 构造极具包容性的 URL 正则表达式字符串
	// 支持带或不带 http(s)://, 带或不带 www., 且匹配后续可能的 URL 路径
	patternStr := fmt.Sprintf(`(?i)\b(?:https?://)?(?:www\.)?(?:%s)(?:/[^\s]*)?`, domainPattern)

	// 4. 立即执行预编译
	compiled, err := regexp.Compile(patternStr)
	if err != nil {
		return nil, fmt.Errorf("compile rule %s failed: %w", name, err)
	}

	// 5. 组装并返回引擎所需的 Rule 实体
	return &Rule{
		Name:       name,
		Pattern:    compiled,
		ActionName: actionName, // 例如 "bili.link.resolve"
		Priority:   100,        // URL 匹配默认给予标准优先级
	}, nil
}
