//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// acquireSingleInstanceLock 确保同一时间只有一个 docutil 实例在运行。
//
// Fyne 的 preferences 存储用 os.Create 直接截断写文件，多实例并发读写同一个
// preferences.json 时会触发 "Preferences load error: Cause: EOF"（无害但吓人）。
// 加一个基于 flock 的锁文件即可从根源上避免多实例共用同一份配置。
//
// 返回一个清理函数（释放锁并关闭文件）和 error。锁文件不会被删除，
// 因为删除会导致后续实例拿到不同 inode 上的锁，破坏互斥性。
func acquireSingleInstanceLock() (func(), error) {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		cfgDir = os.TempDir()
	}
	dir := filepath.Join(cfgDir, "docutil")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	path := filepath.Join(dir, "app.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("另一个 docutil 实例已在运行")
	}

	// 写入 PID 便于排查（失败不影响加锁）。
	_ = f.Truncate(0)
	_, _ = f.WriteString(fmt.Sprintf("%d\n", os.Getpid()))

	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
