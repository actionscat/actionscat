package main

import (
	"actionscat/internal/matcher"
	"fmt"
)

func main() {
	m := matcher.NewSimpleEngine()

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
