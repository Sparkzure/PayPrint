package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
)

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type dataEnvelope struct {
	Data any `json:"data"`
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type listEnvelope struct {
	Data   any   `json:"data"`
	Total  int64 `json:"total"`
	Limit  int   `json:"limit"`
	Offset int   `json:"offset"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)

	if payload == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {

		slog.Default().Error("写出响应失败", "error", err)
	}
}

func writeData(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, dataEnvelope{Data: data})
}

func writeList(w http.ResponseWriter, data any, total int64, limit, offset int) {
	writeJSON(w, http.StatusOK, listEnvelope{
		Data:   data,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}

func writeError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	appErr, ok := domain.AsAppError(err)
	if !ok {
		log.ErrorContext(r.Context(), "未处理的内部错误",
			"method", r.Method, "path", r.URL.Path, "error", err)
		appErr = domain.ErrInternal.WithErr(err)
	} else if appErr.Status >= 500 {
		log.ErrorContext(r.Context(), "服务端错误",
			"method", r.Method, "path", r.URL.Path,
			"code", appErr.Code, "error", appErr.Err)
	} else {
		log.InfoContext(r.Context(), "请求被拒",
			"method", r.Method, "path", r.URL.Path,
			"code", appErr.Code, "message", appErr.Message)
	}

	writeJSON(w, appErr.Status, errorEnvelope{Error: errorBody{
		Code:    appErr.Code,
		Message: appErr.Message,
	}})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	body := http.MaxBytesReader(w, r.Body, maxBytes)

	dec := json.NewDecoder(body)
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return domain.ErrValidation.WithMessage("请求体不能为空")
		}
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return domain.ErrValidation.WithMessage("请求体过大")
		}
		return domain.ErrValidation.WithErr(err)
	}
	return nil
}
