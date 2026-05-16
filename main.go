package main

import (
	"actionscat/internal/api"
	"actionscat/internal/matcher"
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	m := matcher.NewSimpleEngine()

	// 注册bilibili规则
	addr := os.Getenv("ACTIONSCAT_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	router := api.NewRouter()

	log.Printf("ActionsCat core listening on %s", addr)

	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("server stopped: %v", err)
	}

	// 测试匹配
	testUrls := []string{
		"https://www.bilibili.com/video/BV1xx411c7mD",
		"http://bilibili.com/video/BV1xx411c7mD",
		"https://b23.tv/abcdefg",
		"www.bilibili.com",
		"https://www.ithome.com/123/",
		"ithome.com/456",
	}

	for _, url := range testUrls {
		res, ok := m.Match(url)
		if ok {
			fmt.Printf("✓ 匹配成功: %s -> %s\n", url, res.ActionName)
		} else {
			fmt.Printf("✗ 未匹配: %s\n", url)
		}
	}
}
