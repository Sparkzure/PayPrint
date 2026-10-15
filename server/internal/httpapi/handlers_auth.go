package httpapi

import (
	"net/http"
	"strconv"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/notifier"
	"github.com/Sparkzure/PayPrint/server/internal/service"
)

type authResponse struct {
	Token     string           `json:"token"`
	ExpiresAt int64            `json:"expires_at"`
	User      service.UserView `json:"user"`

	Device *service.DeviceView `json:"device,omitempty"`
}

type registerRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func buildDeviceAuthInput(r *http.Request, body []byte) service.DeviceAuthInput {
	timestamp, _ := strconv.ParseInt(r.Header.Get(headerTimestamp), 10, 64)
	return service.DeviceAuthInput{
		Method:      r.Method,
		Path:        r.URL.Path,
		RawQuery:    r.URL.RawQuery,
		Body:        body,
		Fingerprint: r.Header.Get(headerFingerprint),
		Timestamp:   timestamp,
		Nonce:       r.Header.Get(headerNonce),
		Signature:   r.Header.Get(headerSignature),
		PublicKey:   r.Header.Get("X-Device-Public-Key"),
		Platform:    decodeHeaderValue(r.Header.Get("X-Device-Platform")),
		AppVersion:  r.Header.Get("X-Device-App-Version"),
		Label:       decodeHeaderValue(r.Header.Get("X-Device-Label")),
	}
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	body, err := readBodyLimited(w, r, maxBodyBytes)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}

	var req registerRequest
	if err := decodeJSON(w, r, &req, maxBodyBytes); err != nil {
		writeError(w, r, s.log, err)
		return
	}

	result, err := s.svc.Auth.Register(r.Context(), service.RegisterInput{
		Username: req.Username,
		Password: req.Password,
		Device:   buildDeviceAuthInput(r, body),
	})
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}

	writeData(w, http.StatusCreated, authResponse{
		Token:     result.Token,
		ExpiresAt: result.ExpiresAt,
		User:      service.NewUserView(result.User),
		Device:    service.DeviceViewOrNil(result.Device, result.Device.ID),
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	body, err := readBodyLimited(w, r, maxBodyBytes)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}

	var req loginRequest
	if err := decodeJSON(w, r, &req, maxBodyBytes); err != nil {
		writeError(w, r, s.log, err)
		return
	}

	result, err := s.svc.Auth.Login(r.Context(), req.Username, req.Password, buildDeviceAuthInput(r, body))
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}

	user := service.NewUserView(result.User)
	spent, err := s.svc.Payments.TotalSpent(r.Context(), result.User.ID)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	user.TotalSpentFen = spent

	writeData(w, http.StatusOK, authResponse{
		Token:     result.Token,
		ExpiresAt: result.ExpiresAt,
		User:      user,
		Device:    service.DeviceViewOrNil(result.Device, result.Device.ID),
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	session := sessionFrom(r.Context())
	if session == nil {
		writeError(w, r, s.log, domain.ErrUnauthorized)
		return
	}
	if err := s.svc.Auth.Logout(r.Context(), session.Session.ID); err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

type meResponse struct {
	User       service.UserView   `json:"user"`
	Device     service.DeviceView `json:"device"`
	TotalSpent int64              `json:"total_spent_fen"`

	Payment paymentInfo `json:"payment"`
}

type paymentInfo struct {
	ReportPath   string   `json:"report_path"`
	ReportSource string   `json:"report_source"`
	Channels     []string `json:"channels"`
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	session := sessionFrom(r.Context())
	if session == nil {
		writeError(w, r, s.log, domain.ErrUnauthorized)
		return
	}

	spent, err := s.svc.Payments.TotalSpent(r.Context(), session.User.ID)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}

	device := session.Device
	if device == nil {
		writeError(w, r, s.log, domain.ErrSessionHasNoDevice)
		return
	}

	user := service.NewUserView(session.User)
	user.TotalSpentFen = spent

	writeData(w, http.StatusOK, meResponse{
		User:       user,
		Device:     service.NewDeviceView(*device, device.ID),
		TotalSpent: spent,
		Payment: paymentInfo{
			ReportPath:   "/api/v1/payment/report",
			ReportSource: notifier.SourceWeChatAndroid,
			Channels:     []string{string(domain.ChannelWeChat), string(domain.ChannelAlipay)},
		},
	})
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	session := sessionFrom(r.Context())
	if session == nil {
		writeError(w, r, s.log, domain.ErrUnauthorized)
		return
	}
	currentDeviceID := session.DeviceID()
	views, err := s.svc.Devices.List(r.Context(), session.User.ID, currentDeviceID)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeList(w, views, int64(len(views)), len(views), 0)
}

type adminLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type adminLoginResponse struct {
	Token     string           `json:"token"`
	ExpiresAt int64            `json:"expires_at"`
	Admin     service.UserView `json:"admin"`
}

func (s *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	var req adminLoginRequest
	if err := decodeJSON(w, r, &req, 64<<10); err != nil {
		writeError(w, r, s.log, err)
		return
	}
	result, err := s.svc.Auth.AdminLogin(r.Context(), req.Username, req.Password, clientIP(r))
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusOK, adminLoginResponse{
		Token:     result.Token,
		ExpiresAt: result.ExpiresAt,
		Admin:     result.Admin,
	})
}

type adminMeResponse struct {
	ViaToken bool              `json:"via_token"`
	Admin    *service.UserView `json:"admin,omitempty"`
}

func (s *Server) handleAdminMe(w http.ResponseWriter, r *http.Request) {
	actor := adminFrom(r.Context())
	if actor == nil || actor.ViaToken {
		writeData(w, http.StatusOK, adminMeResponse{ViaToken: true})
		return
	}
	session := sessionFrom(r.Context())
	if session == nil {
		writeError(w, r, s.log, domain.ErrUnauthorized)
		return
	}
	view := service.NewUserView(session.User)
	writeData(w, http.StatusOK, adminMeResponse{Admin: &view})
}

func (s *Server) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	session := sessionFrom(r.Context())
	if session != nil {
		if err := s.svc.Auth.Logout(r.Context(), session.Session.ID); err != nil {
			writeError(w, r, s.log, err)
			return
		}
	}

	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLedger(w http.ResponseWriter, r *http.Request) {
	session := sessionFrom(r.Context())
	if session == nil {
		writeError(w, r, s.log, domain.ErrUnauthorized)
		return
	}
	limit := queryInt(r, "limit", 50)
	spent, views, err := s.svc.Payments.LedgerSnapshot(r.Context(), session.User.ID, limit)
	if err != nil {
		writeError(w, r, s.log, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"total_spent_fen": spent,
		"entries":         views,
	})
}
