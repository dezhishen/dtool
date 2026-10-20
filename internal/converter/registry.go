package converter

import (
	"sort"
	"sync"
)

const (
	// AutoConverterName 是「按输入扩展名挑转换器」的显式写法；空串等价于它。
	AutoConverterName = "auto"
	// DefaultConverterName 是内置的 xlsx 转换器名。
	//
	// 注意：auto **没有**兜底转换器——它只按扩展名挑，认不出来就报错。
	// 静默退回某一种格式，会让「我明明给了 .xls」变成「产出了一份空数据集」。
	DefaultConverterName = "xlsx"
)

// registration 是一个转换器的注册项：认哪些扩展名、怎么造实例。
type registration struct {
	exts []string
	new  func() Converter
}

var (
	mu       sync.RWMutex
	registry = map[string]registration{}
	byExt    = map[string]string{} // 扩展名 -> 转换器名，auto 选择用
)

// Register 注册一个转换器，供 --converter / 配置文件按名字选择，也供 auto 按扩展名选择。
//
// 存的是工厂而不是实例：每次解析都新建一个转换器，Configure 收到的配置只作用于
// 这一次执行，两次执行（或并发执行）之间不会串味。
//
// 只在 init 阶段调用（每个转换器在自己的文件里注册自己）；重名、nil 工厂、
// 同一个扩展名被两个转换器认领，都直接 panic——那是构建期错误，留到运行时才发现
// 只会更难查（用户看到的是「有时选错格式」，而不是「代码有问题」）。
func Register(name string, exts []string, newConverter func() Converter) {
	n := normalizeName(name)
	if n == "" {
		panic("converter: 转换器名不能为空")
	}
	if newConverter == nil {
		panic("converter: 转换器 " + n + " 的工厂为 nil")
	}
	norm := make([]string, 0, len(exts))
	for _, e := range exts {
		e = normalizeExt(e)
		if e == "" {
			panic("converter: 转换器 " + n + " 注册了空扩展名")
		}
		norm = append(norm, e)
	}
	if len(norm) == 0 {
		panic("converter: 转换器 " + n + " 没有注册任何扩展名")
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[n]; dup {
		panic("converter: 转换器重复注册：" + n)
	}
	for _, e := range norm {
		if owner, taken := byExt[e]; taken {
			panic("converter: 扩展名 " + e + " 已被 " + owner + " 认领，不能再给 " + n)
		}
	}
	registry[n] = registration{exts: norm, new: newConverter}
	for _, e := range norm {
		byExt[e] = n
	}
}

// Lookup 按名字取工厂；名字大小写与首尾空白不敏感。
func Lookup(name string) (func() Converter, bool) {
	n := normalizeName(name)
	if n == "" {
		return nil, false
	}
	mu.RLock()
	defer mu.RUnlock()
	r, ok := registry[n]
	return r.new, ok
}

// Names 返回已注册的转换器名（升序），用于错误提示与 --help。
func Names() []string {
	mu.RLock()
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	mu.RUnlock()
	sort.Strings(out)
	return out
}

// Extensions 返回所有已注册的扩展名（升序、去重），用于「不支持该文件类型」的提示。
func Extensions() []string {
	mu.RLock()
	out := make([]string, 0, len(byExt))
	for e := range byExt {
		out = append(out, e)
	}
	mu.RUnlock()
	sort.Strings(out)
	return out
}

// ForExtension 按扩展名（大小写不敏感）找转换器工厂。
func ForExtension(ext string) (func() Converter, bool) {
	e := normalizeExt(ext)
	if e == "" {
		return nil, false
	}
	mu.RLock()
	defer mu.RUnlock()
	n, ok := byExt[e]
	if !ok {
		return nil, false
	}
	return registry[n].new, true
}

// unregister 仅供测试清理注册表，避免用例之间互相污染。
func unregister(name string) {
	n := normalizeName(name)
	mu.Lock()
	defer mu.Unlock()
	r, ok := registry[n]
	if !ok {
		return
	}
	delete(registry, n)
	for _, e := range r.exts {
		delete(byExt, e)
	}
}
