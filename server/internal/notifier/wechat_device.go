package notifier

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
)

const SourceWeChatAndroid = "wechat_android"

type WeChatAndroidTrigger struct{}

func (WeChatAndroidTrigger) Name() string { return SourceWeChatAndroid }

type deviceReportPayload struct {
	EventID    string `json:"event_id"`
	Channel    string `json:"channel"`
	OrderNo    string `json:"order_no"`
	OutTradeNo string `json:"out_trade_no"`
	AmountFen  int64  `json:"amount_fen"`
	PaidAt     int64  `json:"paid_at"`
	Nonce      string `json:"nonce"`
	Timestamp  int64  `json:"timestamp"`

	Duplex       string `json:"duplex"`
	ColorMode    string `json:"color_mode"`
	UnitPriceFen int64  `json:"unit_price_fen"`
}

func (WeChatAndroidTrigger) Parse(body []byte) (*Report, error) {
	var payload deviceReportPayload

	strict := json.NewDecoder(bytes.NewReader(body))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&payload); err != nil {
		if err2 := json.Unmarshal(body, &payload); err2 != nil {
			return nil, domain.ErrValidation.WithErr(fmt.Errorf("解析支付上报体: %w", err2))
		}
	}

	if strings.TrimSpace(payload.EventID) == "" {
		return nil, domain.ErrValidation.WithMessage("event_id 不能为空（幂等键）")
	}

	if payload.AmountFen < 0 {
		return nil, domain.ErrValidation.WithMessage("amount_fen 不能为负数")
	}

	channel := domain.PaymentChannel(strings.TrimSpace(payload.Channel))
	if channel == "" {
		channel = domain.ChannelWeChat
	}

	paidAt := payload.PaidAt
	if paidAt <= 0 {
		paidAt = payload.Timestamp
	}

	return &Report{
		EventID:      payload.EventID,
		Source:       SourceWeChatAndroid,
		Channel:      channel,
		OrderNo:      payload.OrderNo,
		OutTradeNo:   payload.OutTradeNo,
		AmountFen:    payload.AmountFen,
		PaidAt:       paidAt,
		Nonce:        payload.Nonce,
		Timestamp:    payload.Timestamp,
		Duplex:       domain.DuplexMode(strings.TrimSpace(payload.Duplex)),
		ColorMode:    domain.ColorMode(strings.TrimSpace(payload.ColorMode)),
		UnitPriceFen: payload.UnitPriceFen,
	}, nil
}

const SourceCollectorAPI = "collector_api"

type CollectorAPITrigger struct{}

func (CollectorAPITrigger) Name() string { return SourceCollectorAPI }

func (CollectorAPITrigger) Parse(body []byte) (*Report, error) {
	report, err := WeChatAndroidTrigger{}.Parse(body)
	if err != nil {
		return nil, err
	}
	report.Source = SourceCollectorAPI
	return report, nil
}
