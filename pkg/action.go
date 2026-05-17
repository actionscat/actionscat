package pkg

import (
	"context"
	"log"
)

type ActionContext struct {
	SenderQQ     string `json:"sender_qq"`
	CurrentGroup string `json:"current_group"`
	RawMsg       string `json:"raw_msg"`
}

type Action interface {
	Name() string
	Execute(ctx context.Context, actCtx *ActionContext) (any, error)
}

var registry = make(map[string]Action)

func RegisterAction(act Action) {
	log.Printf("[core] registering action %s", act.Name())
	registry[act.Name()] = act
}

func GetAction(name string) (Action, bool) {
	act, exists := registry[name]
	return act, exists
}
