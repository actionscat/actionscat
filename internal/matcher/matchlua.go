package matcher

import (
	lua "github.com/yuin/gopher-lua"
	"log"
	"time"
)

const LuaExecutionTimeout = 100 * time.Second

func MatchLua(luaPath, msg string) bool {
	done := make(chan bool, 1)
	var result bool

	go func() {
		L := lua.NewState()
		defer L.Close()

		// set env variable msg
		L.SetGlobal("msg", lua.LString(msg))

		// exec lua file
		if err := L.DoFile(luaPath); err != nil {
			log.Printf("[matchlua] error executing %s: %v", luaPath, err)
			done <- false
			return
		}

		// get function return value (T/F)
		if L.GetTop() > 0 {
			result = L.ToBool(-1)
			L.Pop(1)
		}
		done <- result
	}()

	// 等待执行完成或超时
	select {
	case res := <-done:
		return res
	case <-time.After(LuaExecutionTimeout):
		log.Printf("[matchlua] lua execution timeout for %s (limit: %v)", luaPath, LuaExecutionTimeout)
		return false
	}
}
