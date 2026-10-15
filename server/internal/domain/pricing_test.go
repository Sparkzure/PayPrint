package domain

import "testing"

func TestParsePageRange(t *testing.T) {
	cases := []struct {
		name  string
		spec  string
		total int
		want  []PageRange
	}{
		{name: "空表示全部", spec: "", total: 5, want: []PageRange{{1, 5}}},
		{name: "单页", spec: "3", total: 10, want: []PageRange{{3, 3}}},
		{name: "区间", spec: "2-4", total: 10, want: []PageRange{{2, 4}}},
		{name: "开区间到末尾", spec: "8-", total: 10, want: []PageRange{{8, 10}}},
		{name: "逗号合并连续区间", spec: "1-3,4,5", total: 10, want: []PageRange{{1, 5}}},
		{name: "乱序去重", spec: "5,1,3,3", total: 10, want: []PageRange{{1, 1}, {3, 3}, {5, 5}}},
		{name: "超出上界被裁剪", spec: "9-999", total: 10, want: []PageRange{{9, 10}}},
		{name: "部分越界只保留有效页", spec: "9-99", total: 10, want: []PageRange{{9, 10}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePageRange(tc.spec, tc.total)
			if err != nil {
				t.Fatalf("ParsePageRange(%q) 返回错误: %v", tc.spec, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("ParsePageRange(%q) = %v，期望 %v", tc.spec, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("ParsePageRange(%q) = %v，期望 %v", tc.spec, got, tc.want)
				}
			}
		})
	}
}

func TestParsePageRangeRejectsGarbage(t *testing.T) {
	cases := []struct {
		spec  string
		total int
	}{
		{"abc", 10},
		{"0", 10},
		{"-5", 10},
		{"3-x", 10},
		{"99", 10},
		{"200-300", 10},
	}
	for _, tc := range cases {
		if _, err := ParsePageRange(tc.spec, tc.total); err == nil {
			t.Fatalf("ParsePageRange(%q, %d) 应当报错", tc.spec, tc.total)
		}
	}
}

func TestParsePageRangeNeedsTotal(t *testing.T) {

	if _, err := ParsePageRange("", 0); err == nil {
		t.Fatal("页数未知且未指定范围时应当报错")
	}
}

func TestQuoteBySheet(t *testing.T) {

	const (
		singleBW    = 100
		singleColor = 300
		duplexBW    = 180
		duplexColor = 500
	)
	cases := []struct {
		name       string
		params     PrintParams
		pages      int
		wantUnit   int64
		wantSides  int64
		wantSheets int64
		wantTotal  int64
	}{
		{
			name:      "默认单面黑白",
			params:    DefaultPrintParams(),
			pages:     3,
			wantUnit:  singleBW,
			wantSides: 3, wantSheets: 3, wantTotal: 300,
		},
		{
			name:      "双面黑白按张计费、纸张减半",
			params:    PrintParams{Copies: 2, Duplex: DuplexLongEdge},
			pages:     5,
			wantUnit:  duplexBW,
			wantSides: 5, wantSheets: 3, wantTotal: 180 * 3 * 2,
		},
		{
			name:      "单面彩色走彩色档",
			params:    PrintParams{Copies: 1, ColorMode: ColorModeColor},
			pages:     2,
			wantUnit:  singleColor,
			wantSides: 2, wantSheets: 2, wantTotal: 600,
		},
		{
			name:      "双面彩色走双面彩色档",
			params:    PrintParams{Copies: 1, Duplex: DuplexShortEdge, ColorMode: ColorModeColor},
			pages:     4,
			wantUnit:  duplexColor,
			wantSides: 4, wantSheets: 2, wantTotal: 1000,
		},
		{
			name:      "4 页拼 1 面",
			params:    PrintParams{Copies: 1, NUp: 4},
			pages:     9,
			wantUnit:  singleBW,
			wantSides: 3, wantSheets: 3, wantTotal: 300,
		},
		{
			name:      "页范围裁剪参与计价",
			params:    PrintParams{Copies: 1, PageRange: "1-2"},
			pages:     10,
			wantUnit:  singleBW,
			wantSides: 2, wantSheets: 2, wantTotal: 200,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cost, err := Quote(QuoteInput{
				Params:              tc.params,
				PageCount:           tc.pages,
				PriceSingleBWFen:    singleBW,
				PriceSingleColorFen: singleColor,
				PriceDuplexBWFen:    duplexBW,
				PriceDuplexColorFen: duplexColor,
			})
			if err != nil {
				t.Fatalf("Quote 返回错误: %v", err)
			}
			if cost.UnitPriceFen != tc.wantUnit {
				t.Errorf("每张单价 = %d 分，期望 %d 分", cost.UnitPriceFen, tc.wantUnit)
			}
			if cost.SidesPerCopy != tc.wantSides {
				t.Errorf("每份面数 = %d，期望 %d", cost.SidesPerCopy, tc.wantSides)
			}
			if cost.SheetsPerCopy != tc.wantSheets {
				t.Errorf("每份纸张数 = %d，期望 %d", cost.SheetsPerCopy, tc.wantSheets)
			}
			if cost.TotalFen != tc.wantTotal {
				t.Errorf("总价 = %d 分，期望 %d 分", cost.TotalFen, tc.wantTotal)
			}
			if cost.Explanation == "" {
				t.Error("计价说明不应为空：前端需要它来解释金额")
			}
		})
	}
}

func TestQuoteRejectsUnknownPageCount(t *testing.T) {
	if _, err := Quote(QuoteInput{
		Params: DefaultPrintParams(), PageCount: 0, PriceSingleBWFen: 100,
	}); err == nil {
		t.Fatal("页数为 0 时应当报错")
	}
}

func TestPrintParamsNormalizeAndValidate(t *testing.T) {
	var empty PrintParams
	normalized := empty.Normalize()
	if err := normalized.Validate(); err != nil {
		t.Fatalf("零值参数补齐默认值后应当合法: %v", err)
	}
	if normalized.Copies != 1 || normalized.PaperSize != PaperA4 {
		t.Fatalf("默认值不正确: %+v", normalized)
	}

	bad := DefaultPrintParams()
	bad.Copies = 0
	bad = bad.Normalize()
	if err := bad.Validate(); err != nil {
		t.Fatalf("份数 0 应被补齐为 1: %v", err)
	}

	invalid := DefaultPrintParams()
	invalid.NUp = 3
	if err := invalid.Validate(); err == nil {
		t.Fatal("拼版 3 不合法，应当报错")
	}
}

func TestFormatYuan(t *testing.T) {
	cases := map[int64]string{0: "0.00", 1: "0.01", 100: "1.00", 12345: "123.45", -250: "-2.50"}
	for fen, want := range cases {
		if got := FormatYuan(fen); got != want {
			t.Errorf("FormatYuan(%d) = %q，期望 %q", fen, got, want)
		}
	}
}
