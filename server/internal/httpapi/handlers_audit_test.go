package httpapi

import (
	"testing"

	"github.com/Sparkzure/PayPrint/server/internal/service"
)

func TestAuditCSVRowMatchesHeader(t *testing.T) {
	row := auditCSVRow(service.OrderAuditView{
		Username:          "alice",
		DeviceLabel:       "我的设备",
		DeviceFingerprint: "fp-1",
		OrderNo:           "OC20261012001",
		AmountText:        "6.00",
		FileName:          "报告.pdf",
		ColorText:         "黑白",
		DuplexText:        "单面",
		Copies:            2,
		TotalPages:        6,
		CreatedAt:         1790000000,
	})

	if len(row) != len(auditCSVHeader) {
		t.Fatalf("数据行 %d 列，表头 %d 列", len(row), len(auditCSVHeader))
	}
	want := []string{
		"alice", "我的设备", "fp-1", "OC20261012001", "6.00",
		"报告.pdf", "黑白", "单面", "2", "6",
	}
	for i, value := range want {
		if row[i] != value {
			t.Fatalf("第 %d 列 = %q，期望 %q", i+1, row[i], value)
		}
	}
	if row[len(row)-1] == "" {
		t.Fatal("下单时间不能为空：对账要能按时间排序")
	}
}
