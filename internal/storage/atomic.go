package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ExpandHomePath 将以 ~ 或 ~/ 开头的路径展开为当前用户主目录。
func ExpandHomePath(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if trimmed == "~" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return home
		}
		return trimmed
	}
	if strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, `~\`) {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, trimmed[2:])
		}
	}
	return trimmed
}

// WriteJSONAtomic 以排他临时文件 + fsync + rename 方式原子写入 JSON，防止崩溃损坏文件或权限泄露。
func WriteJSONAtomic(path string, v any, perm os.FileMode) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 JSON 失败: %w", err)
	}
	raw = append(raw, '\n')

	return WriteBytesAtomic(path, raw, perm)
}

// WriteBytesAtomic 将原始字节以排他临时文件（O_EXCL）原子替换方式写入目标路径，并强制应用目标权限。
func WriteBytesAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("创建目录失败 %s: %w", dir, err)
	}

	file, err := os.CreateTemp(dir, fmt.Sprintf(".%s.tmp-*", filepath.Base(path)))
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpPath := file.Name()

	closed := false
	writeErr := func() error {
		if err := file.Chmod(perm); err != nil {
			return err
		}
		if _, err := file.Write(data); err != nil {
			return err
		}
		if err := file.Sync(); err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		closed = true
		if err := os.Chmod(tmpPath, perm); err != nil {
			return err
		}
		if err := os.Rename(tmpPath, path); err != nil {
			// Windows 上目标文件已存在时 Rename 可能失败：先将原文件移至备份路径，替换失败时恢复备份避免丢数据。
			backupPath := fmt.Sprintf("%s.bak-%d-%d", path, os.Getpid(), time.Now().UnixNano())
			if bakErr := os.Rename(path, backupPath); bakErr != nil {
				return err
			}
			if retryErr := os.Rename(tmpPath, path); retryErr != nil {
				_ = os.Rename(backupPath, path)
				return retryErr
			}
			_ = os.Remove(backupPath)
		}
		_ = os.Chmod(path, perm)
		return nil
	}()

	if writeErr != nil {
		if !closed {
			_ = file.Close()
		}
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
