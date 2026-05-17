package matcher

import _ "fmt"

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
