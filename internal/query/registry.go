package query

import (
	"sort"
	"strings"
	"sync"

	"github.com/dezhishen/dtool/pkg/types"
)

// AutoEngineName 是「按默认后端」的显式写法；空串等价于它。
const AutoEngineName = "auto"

var (
	mu       sync.RWMutex
	registry = map[string]func() Engine{}
)

// Register 注册一个后端工厂，供 --engine / 配置文件 engine 按名字选择。
//
// 存的是工厂而不是实例：每次解析都新建一个引擎，Configure 收到的配置只作用于
// 这一次执行，两次执行（或并发执行）之间不会串味。
//
// 只在 init 阶段调用（每个后端在自己的文件里注册自己）；重名或 nil 工厂直接
// panic，因为那是构建期错误——两个文件争同一个名字，留到运行时才发现只会更难查。
func Register(name string, newEngine func() Engine) {
	n := normalizeEngineName(name)
	if n == "" {
		panic("query: 引擎名不能为空")
	}
	if newEngine == nil {
		panic("query: 引擎 " + n + " 的工厂为 nil")
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[n]; dup {
		panic("query: 引擎重复注册：" + n)
	}
	registry[n] = newEngine
}

// Lookup 按名字取工厂；名字大小写与首尾空白不敏感。
func Lookup(name string) (func() Engine, bool) {
	n := normalizeEngineName(name)
	if n == "" {
		return nil, false
	}
	mu.RLock()
	defer mu.RUnlock()
	newEngine, ok := registry[n]
	return newEngine, ok
}

// EngineNames 返回已注册的引擎名（升序），用于错误提示与 --help。
func EngineNames() []string {
	mu.RLock()
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	mu.RUnlock()
	sort.Strings(out)
	return out
}

// ResolveEngine 把配置/参数里的名字解析成一个**新实例**。
// 空串与 "auto" 都表示默认后端（DefaultEngineName）。
func ResolveEngine(name string) (Engine, error) {
	n := normalizeEngineName(name)
	if n == "" {
		n = DefaultEngineName
	}
	newEngine, ok := Lookup(n)
	if !ok {
		return nil, types.Errorf(types.CodeUsage, "unknown engine %q", name).
			WithHint("可用引擎：" + strings.Join(EngineNames(), " / ") +
				"；auto 表示默认的 " + DefaultEngineName)
	}
	eng := newEngine()
	if eng == nil {
		return nil, types.Errorf(types.CodeGeneral, "engine %s 的工厂返回 nil", n)
	}
	return eng, nil
}

// normalizeEngineName 统一名字写法：小写、去空白；auto 归一为空串。
func normalizeEngineName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == AutoEngineName {
		return ""
	}
	return n
}

// unregister 仅供测试清理注册表，避免用例之间互相污染。
func unregister(name string) {
	n := normalizeEngineName(name)
	mu.Lock()
	delete(registry, n)
	mu.Unlock()
}
