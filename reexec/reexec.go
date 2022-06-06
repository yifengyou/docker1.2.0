package reexec

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// 全局变量，字符串函数map，用于注册初始化函数
// 注意，变量开头小写，仅限制在当前包github.com/docker/docker/reexec中
// 很明显，这个目录只有一个go源文件，那么压根没有任何初始化函数在这里注册
var registeredInitializers = make(map[string]func())

// Register adds an initialization func under the specified name
func Register(name string, initializer func()) {
	// 如果已经存在则爆炸退出，map非并发安全，此处不会并发，不需要加锁
	if _, exists := registeredInitializers[name]; exists {
		panic(fmt.Sprintf("reexec func already registred under name %q", name))
	}

	registeredInitializers[name] = initializer
}

// Init is called as the first part of the exec process and returns true if an
// initialization function was called.
func Init() bool {
	// os.Args[0] 为当前程序的完整路径，绝对路径，从/开始
	initializer, exists := registeredInitializers[os.Args[0]]
	if exists {
		initializer()

		return true
	}

	return false
}

// Self returns the path to the current processes binary
func Self() string {
	// 从命令中获取可执行程序的完整路径（如果存在于环境变量中则优先）
	// 这里其实是这样一个逻辑，它希望使用PATH环境变量中优先级较高的匹配项
	name := os.Args[0]

	// filepath.Base 获取路径最后一个元素
	// filepath.Base("/home/gfg") -> gfg
	// filepath.Base(":gfg/GFG")  -> GFG
	if filepath.Base(name) == name {
		// 在当前环境变量中查找二进制文件
		if lp, err := exec.LookPath(name); err == nil {
			name = lp
		}
	}

	return name
}
