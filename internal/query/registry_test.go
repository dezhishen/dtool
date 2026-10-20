package query

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/dezhishen/dtool/pkg/types"
)

func TestDefaultEngineRegistered(t *testing.T) {
	factory, ok := Lookup(DefaultEngineName)
	if !ok {
		t.Fatalf("%s 应已注册，当前注册表 %v", DefaultEngineName, EngineNames())
	}
	eng := factory()
	if eng == nil {
		t.Fatal("默认后端的工厂返回 nil")
	}
	if eng.Name() != DefaultEngineName {
		t.Fatalf("注册名 %q 与实例名 %q 不一致", DefaultEngineName, eng.Name())
	}
	if !slices.Contains(EngineNames(), DefaultEngineName) {
		t.Fatalf("EngineNames() = %v，缺 %s", EngineNames(), DefaultEngineName)
	}
}

func TestResolveEngineName(t *testing.T) {
	// 空串 / auto / 大小写与空白都要落到默认后端
	for _, name := range []string{"", "auto", " AUTO ", "SQLite"} {
		eng, err := ResolveEngine(name)
		if err != nil {
			t.Fatalf("ResolveEngine(%q): %v", name, err)
		}
		if eng.Name() != DefaultEngineName {
			t.Fatalf("ResolveEngine(%q) = %s，期望 %s", name, eng.Name(), DefaultEngineName)
		}
	}

	_, err := ResolveEngine("nope")
	var te *types.Error
	if !errors.As(err, &te) || te.Code != types.CodeUsage {
		t.Fatalf("未知引擎应返回参数错误，实际 %#v", err)
	}
	// 错误信息要能自解释：列出可用后端，用户不用翻文档
	if !strings.Contains(te.Hint, DefaultEngineName) {
		t.Fatalf("hint 应列出可用引擎，实际 %q", te.Hint)
	}
}

func TestRegisterAndUnregister(t *testing.T) {
	const name = "test-only-engine"
	Register(name, func() Engine { return &sqliteEngine{} })
	defer unregister(name)

	if _, ok := Lookup("Test-Only-Engine"); !ok {
		t.Fatal("注册后应按名字（大小写不敏感）查到")
	}
	if _, err := ResolveEngine(name); err != nil {
		t.Fatalf("ResolveEngine(%q): %v", name, err)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	const name = "test-dup-engine"
	Register(name, func() Engine { return &sqliteEngine{} })
	defer unregister(name)
	defer func() {
		if recover() == nil {
			t.Fatal("重名注册应 panic（构建期错误不该留到运行时）")
		}
	}()
	Register(name, func() Engine { return &sqliteEngine{} })
}

// 每次解析都必须是新实例：Configure 写进去的配置只属于这一次执行，
// 不能在两次执行之间串味（否则并发/连续命令会拿到上一条命令的配置）。
func TestResolveReturnsFreshInstance(t *testing.T) {
	a, err := ResolveEngine(DefaultEngineName)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ResolveEngine(DefaultEngineName)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Configure(map[string]any{"cache_size_kb": 123}); err != nil {
		t.Fatal(err)
	}
	sa := a.(*sqliteEngine).dsn(StoreMemory, "")
	sb := b.(*sqliteEngine).dsn(StoreMemory, "")
	if !strings.Contains(sa, "cache_size(-123)") {
		t.Fatalf("配置应作用在 a 上：%s", sa)
	}
	if strings.Contains(sb, "cache_size") {
		t.Fatalf("b 应保持默认配置，实际 %s", sb)
	}
}

func TestRunSelectsEngineByName(t *testing.T) {
	off := uint64(0)
	res, err := Run(context.Background(), Options{
		SQL: "SELECT 1 AS n", MaxMemory: &off, EngineName: "sqlite",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Engine != "sqlite" {
		t.Fatalf("结果里应记录实际后端，实际 %q", res.Engine)
	}

	_, err = Run(context.Background(), Options{
		SQL: "SELECT 1 AS n", MaxMemory: &off, EngineName: "nope",
	})
	var te *types.Error
	if !errors.As(err, &te) || te.Code != types.CodeUsage {
		t.Fatalf("未注册的 engine 名应在执行前报参数错误，实际 %#v", err)
	}
}

func TestRunRejectsEngineConflict(t *testing.T) {
	off := uint64(0)
	// 实例名（fake）与名字（sqlite）指向不同后端：这是接线错误，必须报错而不是
	// 悄悄挑一个。
	_, err := Run(context.Background(), Options{
		SQL: "SELECT 1 AS n", MaxMemory: &off, Engine: &errEngine{}, EngineName: "sqlite",
	})
	var te *types.Error
	if !errors.As(err, &te) || te.Code != types.CodeUsage || !strings.Contains(te.Message, "conflict") {
		t.Fatalf("实例与名字冲突应报参数错误，实际 %#v", err)
	}
}
