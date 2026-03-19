package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var legacyDBArtifacts = []string{"state.db", "ccclaw.db"}

func cleanupLegacyDBFiles(varDir string) error {
	varDir = strings.TrimSpace(varDir)
	if varDir == "" {
		return nil
	}

	found := false
	for _, name := range legacyDBArtifacts {
		if _, err := os.Stat(filepath.Join(varDir, name)); err == nil {
			found = true
			break
		}
	}
	if !found {
		return nil
	}

	archiveDir := filepath.Join(varDir, "archive", "legacy-db")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return fmt.Errorf("创建遗留 SQLite 归档目录失败: %w", err)
	}

	for _, name := range legacyDBArtifacts {
		source := filepath.Join(varDir, name)
		info, err := os.Stat(source)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("读取遗留 SQLite 文件失败: %w", err)
		}
		if !info.Mode().IsRegular() {
			continue
		}

		target := filepath.Join(archiveDir, legacyDBArchiveName(name, time.Now().UTC()))
		if err := os.Rename(source, target); err != nil {
			return fmt.Errorf("归档遗留 SQLite 文件失败: %w", err)
		}
	}
	return nil
}

func legacyDBArchiveName(name string, now time.Time) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	base = strings.ReplaceAll(base, ".", "_")
	return fmt.Sprintf("%s_sqlite_%s.bak", base, now.Format("20060102T150405Z"))
}
