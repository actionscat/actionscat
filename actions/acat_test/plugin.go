package acat_test_action

import (
	"actionscat/internal/api"
	"actionscat/internal/matcher"
	"fmt"
)

func init() {
	// reg executor
	matcher.RegisterExecutor("acat_test", Test)
}

func Test(ctx matcher.ExecutionContext) (any, error) {
	fmt.Println("200 OK")
	return []api.ResponseMessage{
		{Type: "text", Text: "200 OK"},
	}, nil
}
