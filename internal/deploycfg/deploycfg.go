// Package deploycfg 提供部署配置硬化原语：运行环境解析与密钥来源（env 或 _FILE 文件）解析。
// 无状态、无全局态、全离线可测；由控制面/边车 LoadConfig 共享（单一真相源）。
package deploycfg

import (
	"fmt"
	"os"
	"strings"
)

// Environment 是进程运行环境。零值 = Development（向后兼容默认）。
type Environment int

const (
	Development Environment = iota
	Production
)

// IsProduction 报告是否生产环境（触发 fail-close 硬校验）。
func (e Environment) IsProduction() bool { return e == Production }

// String 返回环境的规范名。
func (e Environment) String() string {
	if e == Production {
		return "production"
	}
	return "development"
}

// ParseEnvironment 解析环境字符串（取自 yaml/env，大小写敏感）：
//
//	""            → Development（未设=向后兼容默认）
//	"development" → Development
//	"production"  → Production
//	其它           → 错误（fail-close：拼写错误如 "prod"/"prd" 绝不静默降级为 dev）
func ParseEnvironment(s string) (Environment, error) {
	switch s {
	case "", "development":
		return Development, nil
	case "production":
		return Production, nil
	default:
		return Development, fmt.Errorf("deploycfg: 无法识别的 environment %q（仅接受 development/production）", s)
	}
}

// ResolveSecret 从环境变量 name 或其 name+"_FILE" 变体解析一个密钥值：
//
//	仅 name 设            → 返回 getenv(name)（今天的行为，逐字节不变）
//	仅 name+"_FILE" 设     → 读该路径文件，去尾部空白，返回内容
//	两者同设              → 错误（歧义 fail-close）
//	皆空                  → 返回 ""（调用方按既有必填校验处理）
//
// getenv 注入便于测试；文件经 os.ReadFile 读取。
func ResolveSecret(getenv func(string) string, name string) (string, error) {
	val := getenv(name)
	fileVal := getenv(name + "_FILE")
	if val != "" && fileVal != "" {
		return "", fmt.Errorf("deploycfg: %s 与 %s_FILE 不可同设（歧义）", name, name)
	}
	if fileVal == "" {
		return val, nil
	}
	b, err := os.ReadFile(fileVal)
	if err != nil {
		return "", fmt.Errorf("deploycfg: 读取 %s_FILE 失败: %w", name, err)
	}
	return strings.TrimRight(string(b), " \t\r\n"), nil
}
