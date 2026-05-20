package matcher

import lua "github.com/yuin/gopher-lua"

func MatchLua(luaPath, msg string) bool {
	L := lua.NewState()
	defer L.Close()

	// 设置全局变量 msg
	L.SetGlobal("msg", lua.LString(msg))

	// 执行 lua 文件
	if err := L.DoFile(luaPath); err != nil {
		return false
	}

	// 获取函数返回值（lua 文件最后应该返回 true/false）
	if L.GetTop() > 0 {
		result := L.ToBool(-1)
		L.Pop(1)
		return result
	}

	return false
}
