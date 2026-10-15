package httpapi

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"time"

	"github.com/Sparkzure/PayPrint/server/internal/service"
)

func (s *Server) handleAdminListAudits(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 100)
	offset := queryInt(r, "offset", 0)

	views, total, err := s.svc.Audits.List(r.Context(), limit, offset)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeList(w, views, total, limit, offset)
}

const auditExportPageSize = 500

var auditCSVHeader = []string{
	"账号", "设备名称", "设备指纹", "订单编号", "金额(元)",
	"打印文件名", "色彩", "单双面", "打印份数", "打印总页数", "下单时间",
}

func (s *Server) handleAdminExportAudits(w http.ResponseWriter, r *http.Request) {
	filename := "order-audits-" + s.svc.Now().Format("20060102-150405") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		s.log.WarnContext(r.Context(), "写出审计日志导出内容失败", "error", err)
		return
	}

	cw := csv.NewWriter(w)
	if err := cw.Write(auditCSVHeader); err != nil {
		s.log.WarnContext(r.Context(), "写出审计日志 CSV 表头失败", "error", err)
		return
	}

	for offset := 0; ; offset += auditExportPageSize {
		views, _, err := s.svc.Audits.List(r.Context(), auditExportPageSize, offset)
		if err != nil {

			s.log.ErrorContext(r.Context(), "导出审计日志中途失败", "offset", offset, "error", err)
			return
		}
		for _, view := range views {
			if err := cw.Write(auditCSVRow(view)); err != nil {
				s.log.ErrorContext(r.Context(), "写出审计日志行失败",
					"offset", offset, "order_no", view.OrderNo, "error", err)
				return
			}
		}
		cw.Flush()
		if err := cw.Error(); err != nil {
			s.log.ErrorContext(r.Context(), "刷新审计日志 CSV 失败", "offset", offset, "error", err)
			return
		}
		if len(views) < auditExportPageSize {
			return
		}
	}
}

func auditCSVRow(v service.OrderAuditView) []string {
	return []string{
		v.Username,
		v.DeviceLabel,
		v.DeviceFingerprint,
		v.OrderNo,
		v.AmountText,
		v.FileName,
		v.ColorText,
		v.DuplexText,
		strconv.FormatInt(v.Copies, 10),
		strconv.FormatInt(v.TotalPages, 10),
		time.Unix(v.CreatedAt, 0).Format("2006-01-02 15:04:05"),
	}
}
