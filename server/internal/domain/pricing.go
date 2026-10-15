package domain

import (
	"fmt"
	"strconv"
)

type QuoteInput struct {
	Params              PrintParams
	PageCount           int
	PriceSingleBWFen    int64
	PriceSingleColorFen int64
	PriceDuplexBWFen    int64
	PriceDuplexColorFen int64
	Currency            string
}

type CostBreakdown struct {
	UnitPriceFen  int64       `json:"unit_price_fen"`
	SidesPerCopy  int64       `json:"sides_per_copy"`
	SheetsPerCopy int64       `json:"sheets_per_copy"`
	Copies        int64       `json:"copies"`
	PagesTotal    int64       `json:"pages_total"`
	TotalFen      int64       `json:"total_fen"`
	Currency      string      `json:"currency"`
	ColorMode     ColorMode   `json:"color_mode"`
	NUp           int         `json:"n_up"`
	Duplex        DuplexMode  `json:"duplex"`
	PageRanges    []PageRange `json:"page_ranges"`
	Explanation   string      `json:"explanation"`
}

func Quote(in QuoteInput) (CostBreakdown, error) {
	params := in.Params.Normalize()
	if err := params.Validate(); err != nil {
		return CostBreakdown{}, err
	}
	if in.PageCount <= 0 {
		return CostBreakdown{}, ErrValidation.WithMessage("文件页数必须为正数")
	}

	ranges, err := ParsePageRange(params.PageRange, in.PageCount)
	if err != nil {
		return CostBreakdown{}, err
	}
	pages := int64(Total(ranges))
	if pages <= 0 {
		return CostBreakdown{}, ErrNothingToPrint
	}

	sides := (pages + int64(params.NUp) - 1) / int64(params.NUp)
	if sides < 1 {
		sides = 1
	}

	duplexed := params.Duplex != DuplexOneSided
	sheets := sides
	if duplexed {
		sheets = (sides + 1) / 2
	}

	unit := in.PriceSingleBWFen
	switch {
	case duplexed && params.ColorMode == ColorModeColor:
		unit = in.PriceDuplexColorFen
	case duplexed:
		unit = in.PriceDuplexBWFen
	case params.ColorMode == ColorModeColor:
		unit = in.PriceSingleColorFen
	}

	if unit < 0 {
		return CostBreakdown{}, ErrInternal.WithMessage("打印机单价格式为负，请检查打印机配置")
	}

	currency := in.Currency
	if currency == "" {
		currency = "CNY"
	}

	cost := CostBreakdown{
		UnitPriceFen:  unit,
		SidesPerCopy:  sides,
		SheetsPerCopy: sheets,
		Copies:        int64(params.Copies),
		PagesTotal:    pages,
		TotalFen:      unit * sheets * int64(params.Copies),
		Currency:      currency,
		ColorMode:     params.ColorMode,
		NUp:           params.NUp,
		Duplex:        params.Duplex,
		PageRanges:    ranges,
	}

	cost.Explanation = fmt.Sprintf("%s/张 × %d 张/份 × %s 份 = %s 元",
		formatYuan(unit),
		sheets,
		strconv.Itoa(params.Copies),
		formatYuan(cost.TotalFen),
	)
	if params.NUp > 1 {
		cost.Explanation = fmt.Sprintf("选中 %d 页，%d 页拼 1 面 → ", pages, params.NUp) + cost.Explanation
	}
	return cost, nil
}

func FormatYuan(fen int64) string { return formatYuan(fen) }

func formatYuan(fen int64) string {
	sign := ""
	if fen < 0 {
		sign = "-"
		fen = -fen
	}
	return fmt.Sprintf("%s%d.%02d", sign, fen/100, fen%100)
}
