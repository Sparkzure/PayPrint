package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/Sparkzure/PayPrint/server/internal/backup"
	"github.com/Sparkzure/PayPrint/server/internal/domain"
)

func (s *Server) handleAdminBackup(w http.ResponseWriter, r *http.Request) {
	version, err := s.svc.MigrationVersion(r.Context())
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}

	name := fmt.Sprintf("payprint-backup-%s.zip", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))

	w.WriteHeader(http.StatusOK)

	if err := backup.WriteBackup(r.Context(), s.svc.DB, version, s.cfg.StorageDir, w); err != nil {

		s.log.ErrorContext(r.Context(), "导出备份失败", "error", err)
	}
}

func (s *Server) handleAdminStagedRestore(w http.ResponseWriter, r *http.Request) {
	manifest, err := backup.StagedManifest(s.restoreStageDir())
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	if manifest == nil {
		writeData(w, http.StatusOK, map[string]any{"staged": false})
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"staged":            true,
		"migration_version": manifest.MigrationVersion,
		"created_at":        manifest.CreatedAt,
		"files":             manifest.Files,
	})
}

func (s *Server) handleAdminClearStagedRestore(w http.ResponseWriter, r *http.Request) {
	if err := backup.ClearStaged(s.restoreStageDir()); err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

const maxRestoreBytes = int64(2) << 30

func (s *Server) handleAdminRestore(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeError(w, r, s.log, domain.ErrValidation.WithMessage(
			"请以 multipart 表单上传，字段名为 file"))
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, r, s.log, domain.ErrValidation.WithMessage("缺少 file 字段"))
		return
	}
	defer func() { _ = file.Close() }()
	r.Body = http.MaxBytesReader(w, r.Body, maxRestoreBytes)

	version, err := s.svc.MigrationVersion(r.Context())
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}

	manifest, err := backup.Stage(file, s.restoreStageDir(), version)
	if err != nil {

		if errors.Is(err, backup.ErrInvalid) {
			writeError(w, r, s.log, domain.ErrValidation.WithMessage(err.Error()))
			return
		}
		writeError(w, r, s.log, err)
		return
	}

	writeData(w, http.StatusOK, map[string]any{
		"staged":            true,
		"restart_required":  true,
		"migration_version": manifest.MigrationVersion,
		"created_at":        manifest.CreatedAt,
		"files":             manifest.Files,
		"hint": "校验已通过，备份已暂存。服务将退出以便应用还原；" +
			"容器或 systemd 会把它拉起来，起来之后还原就完成了。",
	})

	if s.OnShutdown != nil {
		s.OnShutdown("管理面板请求应用备份还原")
	}
}

func (s *Server) restoreStageDir() string {
	return filepath.Join(filepath.Dir(s.cfg.DBPath), "restore")
}
