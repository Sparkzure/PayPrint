package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/ids"
	"github.com/Sparkzure/PayPrint/server/internal/pdfutil"
	"github.com/Sparkzure/PayPrint/server/internal/store"
)

const headCaptureLimit = 1024

const maxImageBytes = 20 << 20

type FileService struct{ *Services }

type UploadInput struct {
	UserID       string
	Filename     string
	ContentType  string
	DeclaredSize int64
	Reader       io.Reader

	PageCountHint int
}

func (svc *FileService) Upload(ctx context.Context, in UploadInput) (*PrintFileView, error) {
	limit := svc.Cfg.MaxUploadBytes
	if limit <= 0 {
		limit = 50 << 20
	}
	if in.DeclaredSize > limit {
		return nil, domain.ErrFileTooLarge
	}
	if strings.TrimSpace(in.UserID) == "" {
		return nil, domain.ErrUnauthorized
	}

	dir := filepath.Join(svc.Cfg.StorageDir, in.UserID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, domain.ErrInternal.WithErr(fmt.Errorf("创建文件目录: %w", err))
	}

	fileID := ids.New()
	tmpPath := filepath.Join(dir, "."+fileID+".part")

	tmp, err := os.Create(tmpPath)
	if err != nil {
		return nil, domain.ErrInternal.WithErr(fmt.Errorf("创建临时文件: %w", err))
	}
	discard := func() {
		tmp.Close()
		os.Remove(tmpPath)
	}

	hasher := sha256.New()
	head := newHeadCapture(headCaptureLimit)

	written, err := io.Copy(io.MultiWriter(tmp, hasher, head), io.LimitReader(in.Reader, limit+1))
	if err != nil {
		discard()
		return nil, domain.ErrInternal.WithErr(fmt.Errorf("写入上传文件: %w", err))
	}
	switch {
	case written > limit:
		discard()
		return nil, domain.ErrFileTooLarge
	case written == 0:
		discard()
		return nil, domain.ErrEmptyFile
	}

	if err := tmp.Sync(); err != nil {
		discard()
		return nil, domain.ErrInternal.WithErr(fmt.Errorf("刷盘: %w", err))
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return nil, domain.ErrInternal.WithErr(fmt.Errorf("关闭临时文件: %w", err))
	}

	finalPath := filepath.Join(dir, fileID+".pdf")

	contentType := "application/pdf"
	storedSize := written
	pageCount := 0
	pageSource := domain.PageCountDetected

	switch {
	case pdfutil.LooksLikePDF(head.Bytes()):
		if err := os.Rename(tmpPath, finalPath); err != nil {
			os.Remove(tmpPath)
			return nil, domain.ErrInternal.WithErr(fmt.Errorf("落盘: %w", err))
		}
		count, pageErr := countFilePages(finalPath)
		if pageErr != nil {
			if in.PageCountHint <= 0 {
				os.Remove(finalPath)
				return nil, domain.ErrValidation.WithMessage(
					"无法自动识别该 PDF 的页数，请在 page_count 字段中显式提供页数")
			}

			if in.PageCountHint > domain.MaxPageCount {
				os.Remove(finalPath)
				return nil, domain.ErrTooManyPages.WithMessage(fmt.Sprintf(
					"页数不能超过 %d 页", domain.MaxPageCount))
			}
			svc.Log.WarnContext(ctx, "无法识别 PDF 页数，采用客户端上报值（该页数不可信）",
				"file", fileID, "hint", in.PageCountHint, "error", pageErr)
			pageCount = in.PageCountHint
			pageSource = domain.PageCountDeclared
		} else {
			pageCount = count
		}

	case pdfutil.IsImage(head.Bytes()):
		if written > maxImageBytes {
			os.Remove(tmpPath)
			return nil, domain.ErrFileTooLarge.WithMessage(fmt.Sprintf(
				"图片大小超过上限（%d MB）", maxImageBytes>>20))
		}
		raw, err := os.ReadFile(tmpPath)
		if err != nil {
			os.Remove(tmpPath)
			return nil, domain.ErrInternal.WithErr(fmt.Errorf("读取上传图片: %w", err))
		}
		pdfBytes, err := pdfutil.ImageToPDF(raw)
		if err != nil {
			os.Remove(tmpPath)
			return nil, domain.ErrUnsupportedFile.WithMessage(
				"无法解析该图片，请改用 JPG 或 PNG 格式").WithErr(err)
		}
		if err := os.WriteFile(finalPath, pdfBytes, 0o644); err != nil {
			os.Remove(tmpPath)
			os.Remove(finalPath)
			return nil, domain.ErrInternal.WithErr(fmt.Errorf("写入转换后的 PDF: %w", err))
		}
		os.Remove(tmpPath)

		pageCount = 1
		pageSource = domain.PageCountDetected
		storedSize = int64(len(pdfBytes))
		svc.Log.InfoContext(ctx, "图片已转换为单页 PDF",
			"file", fileID, "source_bytes", written, "pdf_bytes", storedSize)

	default:
		os.Remove(tmpPath)
		return nil, domain.ErrUnsupportedFile
	}

	absPath, err := filepath.Abs(finalPath)
	if err != nil {
		absPath = finalPath
	}

	row, err := svc.Q.CreatePrintFile(ctx, store.CreatePrintFileParams{
		ID:              fileID,
		UserID:          in.UserID,
		OriginalName:    sanitizeFilename(in.Filename),
		ContentType:     contentType,
		SizeBytes:       storedSize,
		Sha256:          hex.EncodeToString(hasher.Sum(nil)),
		StoragePath:     absPath,
		PageCount:       int64(pageCount),
		PageCountSource: string(pageSource),
		CreatedAt:       svc.nowUnix(),
	})
	if err != nil {
		os.Remove(finalPath)
		return nil, domain.ErrInternal.WithErr(err)
	}

	view := NewPrintFileView(row)
	return &view, nil
}

func (svc *FileService) List(ctx context.Context, userID string, limit int64) ([]PrintFileView, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := svc.Q.ListPrintFilesByUser(ctx, store.ListPrintFilesByUserParams{
		UserID: userID,
		Limit:  limit,
	})
	if err != nil {
		return nil, domain.ErrInternal.WithErr(err)
	}
	views := make([]PrintFileView, 0, len(rows))
	for _, r := range rows {
		views = append(views, NewPrintFileView(r))
	}
	return views, nil
}

func (svc *FileService) GetForUser(ctx context.Context, userID, fileID string) (store.PrintFile, error) {
	file, err := svc.Q.GetPrintFileForUser(ctx, store.GetPrintFileForUserParams{
		ID:     fileID,
		UserID: userID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.PrintFile{}, domain.ErrFileNotFound
		}
		return store.PrintFile{}, domain.ErrInternal.WithErr(err)
	}
	return file, nil
}

func (svc *FileService) Open(p store.PrintFile) (*os.File, error) {
	f, err := os.Open(p.StoragePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, domain.ErrFileNotFound.WithErr(err)
		}
		return nil, domain.ErrInternal.WithErr(err)
	}
	return f, nil
}

func countFilePages(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return pdfutil.CountPages(f)
}

type headCapture struct {
	limit int
	buf   []byte
}

func newHeadCapture(limit int) *headCapture {
	return &headCapture{limit: limit, buf: make([]byte, 0, limit)}
}

func (h *headCapture) Write(p []byte) (int, error) {
	if len(h.buf) < h.limit {
		remaining := h.limit - len(h.buf)
		if len(p) < remaining {
			h.buf = append(h.buf, p...)
		} else {
			h.buf = append(h.buf, p[:remaining]...)
		}
	}
	return len(p), nil
}

func (h *headCapture) Bytes() []byte { return h.buf }

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "/", "_")

	cleaned := make([]rune, 0, len(name))
	for _, r := range name {
		if unicode.IsControl(r) {
			continue
		}
		cleaned = append(cleaned, r)
	}
	name = strings.TrimSpace(string(cleaned))
	if name == "" {
		return "未命名.pdf"
	}

	return truncate(name, 120)
}
