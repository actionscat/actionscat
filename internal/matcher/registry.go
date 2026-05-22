package matcher

import (
	"log"
)

// 1. 全局的匹配引擎
var GlobalEngine = &SimpleEngine{
	rules: []*Rule{},
}

// 2. 插件执行器函数签名：接收原始请求和执行上下文，返回处理结果
type ExecutionContext struct {
	RawMsg       string
	SenderQQ     string
	CurrentGroup string
}

type ExecutorFunc func(ctx ExecutionContext) (any, error)

// 3. 全局的执行器注册表：通过 ActionName 映射到具体的执行函数
var executorRegistry = make(map[string]ExecutorFunc)

// 注册执行器
func RegisterExecutor(actionName string, exec ExecutorFunc) {
	executorRegistry[actionName] = exec
	log.Printf("[registry] executor registered: %s", actionName)
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

func NewRule(name string, actionName string, rules ...string) (*Rule, error) {
	// 组装并返回引擎所需的 Rule 实体
	return &Rule{
		Name:       name,
		ActionName: actionName,
		rules:      rules,
	}, nil
}
