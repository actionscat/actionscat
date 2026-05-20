package acat_test_action

import (
	"actionscat/internal/api"
	"actionscat/internal/matcher"
	"fmt"
)

/*
type testAction struct{}

func (a *testAction) Name() string {
	return "acat.tech.furryy.test"
}
*/

func init() {
	// reg executor
	matcher.RegisterExecutor("acat_test", Test)
}

func Test(rawMsg string) (any, error) {
	fmt.Println("200 OK")
	return []api.ResponseMessage{
		{Type: "text", Text: "200 OK"},
	}, nil
}
