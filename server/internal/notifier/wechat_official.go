package notifier

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
)

type WeChatV2Config struct {
	AppID    string
	MchID    string
	APIKey   string
	QueryURL string
	Timeout  time.Duration
}

func (c WeChatV2Config) Enabled() bool {
	return c.AppID != "" && c.MchID != "" && c.APIKey != ""
}

type xmlMap struct {
	XMLName xml.Name
	Values  []xmlField `xml:",any"`
}

type xmlField struct {
	XMLName xml.Name
	Value   string `xml:",chardata"`
}

func (m *xmlMap) ToMap() map[string]string {
	out := make(map[string]string, len(m.Values))
	for _, f := range m.Values {
		out[f.XMLName.Local] = f.Value
	}
	return out
}

func buildXML(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	sb.WriteString("<xml>")
	for _, k := range keys {
		v := values[k]
		if v == "" {
			continue
		}
		sb.WriteString("<" + k + ">")
		sb.WriteString(escapeXML(v))
		sb.WriteString("</" + k + ">")
	}
	sb.WriteString("</xml>")
	return sb.String()
}

func escapeXML(s string) string {
	var sb strings.Builder
	_ = xml.EscapeText(&sb, []byte(s))
	return sb.String()
}

func signV2(values map[string]string, apiKey string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		if k == "sign" || values[k] == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteString("&")
		}
		sb.WriteString(k + "=" + values[k])
	}
	sb.WriteString("&key=" + apiKey)

	sum := md5.Sum([]byte(sb.String()))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

type WeChatOrderQuerier struct {
	cfg    WeChatV2Config
	client *http.Client
}

func NewWeChatOrderQuerier(cfg WeChatV2Config) *WeChatOrderQuerier {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	url := cfg.QueryURL
	if url == "" {
		url = "https://api.mch.weixin.qq.com/pay/orderquery"
	}
	cfg.QueryURL = url
	return &WeChatOrderQuerier{
		cfg:    cfg,
		client: &http.Client{Timeout: timeout},
	}
}

func (*WeChatOrderQuerier) Name() string { return "wechat_v2_orderquery" }

func (q *WeChatOrderQuerier) Query(ctx context.Context, orderNo, outTradeNo string) (*QueryResult, error) {
	if !q.cfg.Enabled() {
		return &QueryResult{State: QueryUnknown, TradeState: "not_configured"}, nil
	}

	params := map[string]string{
		"appid":     q.cfg.AppID,
		"mch_id":    q.cfg.MchID,
		"nonce_str": nonceString(),
	}
	if orderNo != "" {
		params["out_trade_no"] = orderNo
	} else if outTradeNo != "" {
		params["transaction_id"] = outTradeNo
	} else {
		return nil, domain.ErrValidation.WithMessage("查单需要 out_trade_no 或 transaction_id")
	}
	params["sign"] = signV2(params, q.cfg.APIKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, q.cfg.QueryURL,
		strings.NewReader(buildXML(params)))
	if err != nil {
		return nil, fmt.Errorf("构造查单请求: %w", err)
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")

	resp, err := q.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用微信查单接口: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取查单响应: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("微信查单 HTTP %d: %s", resp.StatusCode, string(raw))
	}

	var doc xmlMap
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("解析查单响应: %w", err)
	}
	values := doc.ToMap()

	result := &QueryResult{
		TradeState:    values["trade_state"],
		TransactionID: values["transaction_id"],
		OutTradeNo:    values["out_trade_no"],
		Raw:           string(raw),
	}
	if fee, err := strconv.ParseInt(values["total_fee"], 10, 64); err == nil {
		result.AmountFen = fee
	}
	result.PaidAt = parseWeChatTime(values["time_end"])

	switch values["trade_state"] {
	case "SUCCESS":
		result.State = QueryPaid
	case "NOTPAY", "USERPAYING":
		result.State = QueryUnpaid
	case "CLOSED", "REVOKED", "PAYERROR":
		result.State = QueryClosed
	case "REFUND":
		result.State = QueryRefund
	default:

		result.State = QueryUnknown
	}
	return result, nil
}

func parseWeChatTime(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	loc := time.FixedZone("CST", 8*3600)
	for _, layout := range []string{"20060102150405", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t.Unix()
		}
	}
	return 0
}

func nonceString() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}
