package backup

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const FormatV1 = "payprint-backup/v1"

var ErrInvalid = errors.New("invalid backup")

type Manifest struct {
	Format           string `json:"format"`
	MigrationVersion int64  `json:"migration_version"`
	CreatedAt        int64  `json:"created_at"`
	Files            int    `json:"files"`
}

const (
	manifestName = "manifest.json"
	dbName       = "payprint.db"
	filesPrefix  = "files/"
)

func WriteBackup(
	ctx context.Context, db *sql.DB, migrationVersion int64, storageDir string, w io.Writer,
) error {
	snapshot, err := os.CreateTemp("", "payprint-snapshot-*.db")
	if err != nil {
		return fmt.Errorf("创建临时快照: %w", err)
	}
	snapshotPath := snapshot.Name()

	if err := snapshot.Close(); err != nil {
		return err
	}
	defer func() { _ = os.Remove(snapshotPath) }()

	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", snapshotPath); err != nil {
		return fmt.Errorf("生成数据库快照: %w", err)
	}

	fileCount := 0
	if err := filepath.WalkDir(storageDir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			fileCount++
		}
		return nil
	}); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("扫描上传目录: %w", err)
		}

	}

	zw := zip.NewWriter(w)
	if err := writeJSONEntry(zw, manifestName, Manifest{
		Format:           FormatV1,
		MigrationVersion: migrationVersion,
		CreatedAt:        time.Now().Unix(),
		Files:            fileCount,
	}); err != nil {
		return err
	}
	if err := addFileEntry(zw, snapshotPath, dbName); err != nil {
		return fmt.Errorf("写入数据库快照: %w", err)
	}
	if err := addDirectory(zw, storageDir, filesPrefix); err != nil {
		return fmt.Errorf("写入上传文件: %w", err)
	}
	return zw.Close()
}

func Stage(r io.Reader, stageDir string, currentMigration int64) (*Manifest, error) {
	payloadDir := filepath.Join(stageDir, "payload")
	if err := os.RemoveAll(stageDir); err != nil {
		return nil, fmt.Errorf("清理暂存目录: %w", err)
	}
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return nil, err
	}

	tmpZip := filepath.Join(stageDir, "upload.zip.tmp")
	if err := writeAll(tmpZip, r); err != nil {
		return nil, err
	}

	reader, err := zip.OpenReader(tmpZip)
	if err != nil {
		_ = os.Remove(tmpZip)
		return nil, fmt.Errorf("%w: 不是有效的 zip 备份: %v", ErrInvalid, err)
	}
	defer func() { _ = reader.Close() }()

	var manifest Manifest
	seen := map[string]bool{}
	for _, f := range reader.File {
		name := f.Name
		if err := checkEntryName(name); err != nil {
			_ = os.Remove(tmpZip)
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		if name == manifestName {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			if err := json.NewDecoder(rc).Decode(&manifest); err != nil {
				_ = rc.Close()
				_ = os.Remove(tmpZip)
				return nil, fmt.Errorf("%w: 备份清单无法解析: %v", ErrInvalid, err)
			}
			_ = rc.Close()
		}
		seen[name] = true
	}
	_ = os.Remove(tmpZip)

	if err := validateManifest(&manifest, currentMigration); err != nil {
		return nil, err
	}
	if !seen[dbName] {
		return nil, fmt.Errorf("%w: 备份里缺少 %s", ErrInvalid, dbName)
	}

	for _, f := range reader.File {
		target := filepath.Join(payloadDir, filepath.FromSlash(f.Name))
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return nil, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, err
		}
		if err := extractEntry(f, target); err != nil {
			return nil, err
		}
	}
	return &manifest, nil
}

func ApplyStagedRestore(dbPath, storageDir, stageDir string) (bool, error) {
	payloadDir := filepath.Join(stageDir, "payload")
	stagedDB := filepath.Join(payloadDir, dbName)
	if _, err := os.Stat(stagedDB); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}

	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(dbPath + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
	}
	if err := replaceFile(dbPath, stagedDB); err != nil {
		return false, fmt.Errorf("换上备份的数据库: %w", err)
	}

	stagedFiles := filepath.Join(payloadDir, "files")
	if _, err := os.Stat(stagedFiles); err == nil {
		if err := replaceDir(storageDir, stagedFiles); err != nil {
			return false, fmt.Errorf("换上备份的上传文件: %w", err)
		}
	}

	if err := os.RemoveAll(stageDir); err != nil {
		return true, fmt.Errorf("清理暂存目录: %w", err)
	}
	return true, nil
}

func StagedManifest(stageDir string) (*Manifest, error) {
	rc, err := os.Open(filepath.Join(stageDir, "payload", manifestName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	var manifest Manifest
	if err := json.NewDecoder(rc).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("读取待应用备份的清单: %w", err)
	}
	return &manifest, nil
}

func ClearStaged(stageDir string) error {
	if err := os.RemoveAll(stageDir); err != nil {
		return fmt.Errorf("清理暂存目录: %w", err)
	}
	return nil
}

func checkEntryName(name string) error {
	if name == manifestName || name == dbName || strings.HasPrefix(name, filesPrefix) {
		return nil
	}
	return fmt.Errorf("备份包里出现了意料之外的条目：%q", name)
}

func validateManifest(m *Manifest, currentMigration int64) error {
	if m.Format != FormatV1 {
		return fmt.Errorf("%w: 不认识的备份格式 %q（当前支持 %s）", ErrInvalid, m.Format, FormatV1)
	}
	if m.MigrationVersion <= 0 {
		return fmt.Errorf("%w: 备份清单缺少数据库版本", ErrInvalid)
	}

	if m.MigrationVersion > currentMigration {
		return fmt.Errorf(
			"%w: 备份的数据库结构（版本 %d）比当前服务（版本 %d）新，请先升级服务再还原",
			ErrInvalid, m.MigrationVersion, currentMigration)
	}
	return nil
}

func writeJSONEntry(zw *zip.Writer, name string, value any) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func addFileEntry(zw *zip.Writer, path, name string) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: info.ModTime()}
	dst, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	return err
}

func addDirectory(zw *zip.Writer, root, prefix string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {

			if errors.Is(err, fs.ErrNotExist) && path == root {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return addFileEntry(zw, path, prefix+filepath.ToSlash(rel))
	})
}

func extractEntry(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	dst, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = dst.Close() }()
	_, err = io.Copy(dst, rc)
	return err
}

func writeAll(path string, r io.Reader) error {
	dst, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, r); err != nil {
		_ = dst.Close()
		return fmt.Errorf("接收上传内容: %w", err)
	}
	return dst.Close()
}

func replaceFile(dst, src string) error {
	if err := os.Remove(dst + ".pre-restore"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(dst, dst+".pre-restore"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Rename(src, dst)
}

func replaceDir(dst, src string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Remove(dst + ".pre-restore"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(dst, dst+".pre-restore"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Rename(src, dst)
}
