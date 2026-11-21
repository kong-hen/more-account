package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// WriteJSONAtomic 以临时文件 + fsync + rename 方式原子写入 JSON，防止崩溃损坏文件。
func WriteJSONAtomic(path string, v any, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("创建目录失败 %s: %w", dir, err)
	}

	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 JSON 失败: %w", err)
	}
	raw = append(raw, '\n')

	return WriteBytesAtomic(path, raw, perm)
}

// WriteBytesAtomic 将原始字节以原子替换方式写入目标路径。
func WriteBytesAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("创建目录失败 %s: %w", dir, err)
	}

	tmpPath := filepath.Join(dir, fmt.Sprintf(".%s.tmp-%d-%d", filepath.Base(path), os.Getpid(), time.Now().UnixNano()))
	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}

	writeErr := func() error {
		if _, err := file.Write(data); err != nil {
			return err
		}
		if err := file.Sync(); err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		if err := os.Chmod(tmpPath, perm); err != nil {
			return err
		}
		if err := os.Rename(tmpPath, path); err != nil {
			// Windows 上目标文件已存在时 Rename 可能失败，退化为先删后移。
			_ = os.Remove(path)
			return os.Rename(tmpPath, path)
		}
		return nil
	}()

	if writeErr != nil {
		_ = file.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("写入文件失败 %s: %w", path, writeErr)
	}
	return nil
}

// ReadJSONIfExists 读取指定路径的 JSON 文件；文件不存在时返回 (false, nil)。
func ReadJSONIfExists(path string, out any) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("读取文件失败 %s: %w", path, err)
	}
	if len(raw) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return false, fmt.Errorf("解析 JSON 失败 %s: %w", path, err)
	}
	return true, nil
}
