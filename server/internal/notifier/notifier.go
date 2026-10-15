package notifier

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
)

type Report struct {
	EventID    string
	Source     string
	Channel    domain.PaymentChannel
	OrderNo    string
	OutTradeNo string
	AmountFen  int64

	Duplex       domain.DuplexMode
	ColorMode    domain.ColorMode
	UnitPriceFen int64

	PaidAt int64

	Nonce     string
	Timestamp int64
	Raw       []byte
}

type Trigger interface {
	Name() string

	Parse(body []byte) (*Report, error)
}

type Registry struct {
	mu       sync.RWMutex
	triggers map[string]Trigger
}

func NewRegistry() *Registry {
	return &Registry{triggers: make(map[string]Trigger)}
}

func (r *Registry) Register(t Trigger) {
	if t == nil || t.Name() == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.triggers[t.Name()] = t
}

func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.triggers))
	for name := range r.triggers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (r *Registry) Get(name string) (Trigger, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.triggers[name]
	return t, ok
}

func (r *Registry) Parse(source string, body []byte) (*Report, error) {
	r.mu.RLock()
	t, ok := r.triggers[source]
	r.mu.RUnlock()
	if !ok {
		return nil, domain.ErrValidation.WithMessage(
			fmt.Sprintf("未知的支付上报来源 %q，可用来源：%v", source, r.Names()))
	}
	report, err := t.Parse(body)
	if err != nil {
		return nil, err
	}
	if report.Source == "" {
		report.Source = t.Name()
	}
	report.Raw = body
	return report, nil
}

type QueryState string

const (
	QueryPaid    QueryState = "paid"
	QueryUnpaid  QueryState = "unpaid"
	QueryClosed  QueryState = "closed"
	QueryRefund  QueryState = "refund"
	QueryUnknown QueryState = "unknown"
)

type QueryResult struct {
	State         QueryState
	AmountFen     int64
	OutTradeNo    string
	TransactionID string
	TradeState    string
	PaidAt        int64
	Raw           string
}

type OrderQuerier interface {
	Name() string
	Query(ctx context.Context, orderNo, outTradeNo string) (*QueryResult, error)
}

type NopQuerier struct{}

func (NopQuerier) Name() string { return "none" }

func (NopQuerier) Query(context.Context, string, string) (*QueryResult, error) {
	return &QueryResult{State: QueryUnknown, TradeState: "not_configured"}, nil
}

type Queriers struct {
	mu       sync.RWMutex
	fallback OrderQuerier
	byKey    map[string]OrderQuerier
}

func NewQueriers(fallback OrderQuerier) *Queriers {
	if fallback == nil {
		fallback = NopQuerier{}
	}
	return &Queriers{fallback: fallback, byKey: make(map[string]OrderQuerier)}
}

func (q *Queriers) Register(channel string, querier OrderQuerier) {
	if channel == "" || querier == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.byKey[channel] = querier
}

func (q *Queriers) For(channel string) OrderQuerier {
	q.mu.RLock()
	defer q.mu.RUnlock()
	if querier, ok := q.byKey[channel]; ok {
		return querier
	}
	return q.fallback
}
