package updater

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var retryDelay = 200 * time.Millisecond

func retry(f func() error) error {
	var err error
	for i := 1; i <= 5; i++ {
		if err = f(); err == nil {
			return nil
		}
		time.Sleep(time.Duration(i) * retryDelay)
	}
	return err
}

// replaceExecutable 用 newFile 替换 exe。
// Unix：同目录 rename 原子覆盖，正在运行的进程不受影响。
// Windows：运行中的 exe 不能被覆盖或删除，但可以改名：先把旧文件挪到 .old，再把新文件改名到位，
// 失败则回滚；.old 留待下次启动由 CleanupOld 清理。杀软/索引器可能短暂占用文件，所以改名带重试。
func replaceExecutable(exe, newFile string, windows bool) error {
	if !windows {
		return os.Rename(newFile, exe)
	}
	old := exe + ".old"
	if err := os.Remove(old); err != nil && !os.IsNotExist(err) {
		old = fmt.Sprintf("%s.old.%d", exe, time.Now().UnixNano()) // 上一次的旧文件仍被占用
	}
	if err := retry(func() error { return os.Rename(exe, old) }); err != nil {
		return fmt.Errorf("move current binary aside: %w", err)
	}
	if err := retry(func() error { return os.Rename(newFile, exe) }); err != nil {
		if rerr := os.Rename(old, exe); rerr != nil {
			return fmt.Errorf("%w (rollback failed: %v; restore manually from %s)", err, rerr, old)
		}
		return err
	}
	return nil
}

// CleanupOld 尽力删除上次升级遗留的 <exe>.old*（Windows 上旧进程退出后才删得掉）。
func CleanupOld() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	cleanupOld(exe)
}

func cleanupOld(exe string) {
	matches, _ := filepath.Glob(exe + ".old*")
	for _, m := range matches {
		_ = os.Remove(m)
	}
}
