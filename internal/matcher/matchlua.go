package matcher

import lua "github.com/yuin/gopher-lua"

func MatchLua(luaPath, msg string) bool {
	L := lua.NewState()
	defer L.Close()

	// set env variable msg
	L.SetGlobal("msg", lua.LString(msg))

	// exec lua file
	if err := L.DoFile(luaPath); err != nil {
		return false
	}

	// get function return value (T/F)
	if L.GetTop() > 0 {
		result := L.ToBool(-1)
		L.Pop(1)
		return result
	}

	return false
}
