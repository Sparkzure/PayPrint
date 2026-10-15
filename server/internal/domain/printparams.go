package domain

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type DuplexMode string

const (
	DuplexOneSided  DuplexMode = "one_sided"
	DuplexLongEdge  DuplexMode = "long_edge"
	DuplexShortEdge DuplexMode = "short_edge"
)

func (d DuplexMode) Label() string {
	switch d {
	case DuplexOneSided:
		return "单面"
	case DuplexLongEdge:
		return "双面（长边）"
	case DuplexShortEdge:
		return "双面（短边）"
	}
	return string(d)
}

type ColorMode string

const (
	ColorModeMonochrome ColorMode = "monochrome"
	ColorModeColor      ColorMode = "color"
)

func (c ColorMode) Label() string {
	switch c {
	case ColorModeMonochrome:
		return "黑白"
	case ColorModeColor:
		return "彩色"
	}
	return string(c)
}

type PaperSize string

const (
	PaperA4     PaperSize = "A4"
	PaperA5     PaperSize = "A5"
	PaperA3     PaperSize = "A3"
	PaperLetter PaperSize = "Letter"
)

type Orientation string

const (
	OrientationPortrait  Orientation = "portrait"
	OrientationLandscape Orientation = "landscape"
)

type PrintQuality string

const (
	QualityDraft  PrintQuality = "draft"
	QualityNormal PrintQuality = "normal"
	QualityHigh   PrintQuality = "high"
)

var SupportedNUp = []int{1, 2, 4, 6, 9}

const MaxPageCount = 5000

type PrintParams struct {
	Copies      int          `json:"copies"`
	Duplex      DuplexMode   `json:"duplex"`
	ColorMode   ColorMode    `json:"color_mode"`
	PaperSize   PaperSize    `json:"paper_size"`
	PageRange   string       `json:"page_range"`
	NUp         int          `json:"n_up"`
	Orientation Orientation  `json:"orientation"`
	Quality     PrintQuality `json:"quality"`
	FitToPage   bool         `json:"fit_to_page"`
}

func DefaultPrintParams() PrintParams {
	return PrintParams{
		Copies:      1,
		Duplex:      DuplexOneSided,
		ColorMode:   ColorModeMonochrome,
		PaperSize:   PaperA4,
		NUp:         1,
		Orientation: OrientationPortrait,
		Quality:     QualityNormal,
		FitToPage:   true,
	}
}

func (p PrintParams) Normalize() PrintParams {
	def := DefaultPrintParams()
	if p.Copies == 0 {
		p.Copies = def.Copies
	}
	if p.Duplex == "" {
		p.Duplex = def.Duplex
	}
	if p.ColorMode == "" {
		p.ColorMode = def.ColorMode
	}
	if p.PaperSize == "" {
		p.PaperSize = def.PaperSize
	}
	if p.NUp == 0 {
		p.NUp = def.NUp
	}
	if p.Orientation == "" {
		p.Orientation = def.Orientation
	}
	if p.Quality == "" {
		p.Quality = def.Quality
	}
	p.PageRange = strings.TrimSpace(p.PageRange)
	return p
}

func (p PrintParams) Validate() error {
	if p.Copies < 1 || p.Copies > 999 {
		return ErrValidation.WithMessage("份数必须在 1 - 999 之间")
	}
	switch p.Duplex {
	case DuplexOneSided, DuplexLongEdge, DuplexShortEdge:
	default:
		return ErrValidation.WithMessage("单双面取值不合法")
	}
	switch p.ColorMode {
	case ColorModeMonochrome, ColorModeColor:
	default:
		return ErrValidation.WithMessage("色彩模式取值不合法")
	}
	switch p.PaperSize {
	case PaperA4, PaperA5, PaperA3, PaperLetter:
	default:
		return ErrValidation.WithMessage("纸张规格取值不合法")
	}
	if !containsInt(SupportedNUp, p.NUp) {
		return ErrValidation.WithMessage("拼版仅支持 1 / 2 / 4 / 6 / 9")
	}
	switch p.Orientation {
	case OrientationPortrait, OrientationLandscape:
	default:
		return ErrValidation.WithMessage("页面方向取值不合法")
	}
	switch p.Quality {
	case QualityDraft, QualityNormal, QualityHigh:
	default:
		return ErrValidation.WithMessage("打印质量取值不合法")
	}
	return nil
}

type PageRange struct {
	From int `json:"from"`
	To   int `json:"to"`
}

func (r PageRange) Len() int {
	if r.To < r.From {
		return 0
	}
	return r.To - r.From + 1
}

func (r PageRange) String() string {
	if r.From == r.To {
		return strconv.Itoa(r.From)
	}
	return fmt.Sprintf("%d-%d", r.From, r.To)
}

func Total(ranges []PageRange) int {
	n := 0
	for _, r := range ranges {
		n += r.Len()
	}
	return n
}

func ParsePageRange(spec string, total int) ([]PageRange, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		if total <= 0 {
			return nil, ErrNothingToPrint.WithMessage("无法确定文件页数，请指定打印页范围")
		}
		return []PageRange{{From: 1, To: total}}, nil
	}

	var out []PageRange
	seen := map[int]bool{}

	for _, raw := range strings.Split(spec, ",") {
		part := strings.TrimSpace(raw)
		if part == "" {
			continue
		}

		from, to, err := parseRangePart(part, total)
		if err != nil {
			return nil, err
		}
		if to < from {
			continue
		}
		for p := from; p <= to; p++ {
			seen[p] = true
		}
	}

	if len(seen) == 0 {
		return nil, ErrNothingToPrint
	}

	pages := make([]int, 0, len(seen))
	for p := range seen {
		pages = append(pages, p)
	}
	sort.Ints(pages)

	out = append(out, PageRange{From: pages[0], To: pages[0]})
	for _, p := range pages[1:] {
		last := &out[len(out)-1]
		if p == last.To+1 {
			last.To = p
			continue
		}
		out = append(out, PageRange{From: p, To: p})
	}
	return out, nil
}

func parseRangePart(part string, total int) (int, int, error) {

	if strings.HasPrefix(part, "-") {
		return 0, 0, ErrValidation.WithMessage("页范围格式不正确：" + part)
	}

	if !strings.Contains(part, "-") {
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 {
			return 0, 0, ErrValidation.WithMessage("页范围格式不正确：" + part)
		}

		if n > MaxPageCount {
			return 0, 0, ErrPageRangeTooLarge
		}
		if total > 0 && n > total {
			return 1, 0, nil
		}
		return n, n, nil
	}

	pieces := strings.SplitN(part, "-", 2)
	fromStr := strings.TrimSpace(pieces[0])
	toStr := strings.TrimSpace(pieces[1])

	from := 1
	if fromStr != "" {
		n, err := strconv.Atoi(fromStr)
		if err != nil || n < 1 {
			return 0, 0, ErrValidation.WithMessage("页范围格式不正确：" + part)
		}
		from = n
	}

	to := 0
	if toStr != "" {
		n, err := strconv.Atoi(toStr)
		if err != nil || n < 1 {
			return 0, 0, ErrValidation.WithMessage("页范围格式不正确：" + part)
		}
		to = n
	} else if total <= 0 {
		return 0, 0, ErrValidation.WithMessage("无法确定文件页数，请给出明确的页范围上界")
	} else {
		to = total
	}

	if to > MaxPageCount {
		return 0, 0, ErrPageRangeTooLarge
	}
	if total > 0 && to > total {
		to = total
	}
	if from > MaxPageCount {
		return 0, 0, ErrPageRangeTooLarge
	}
	return from, to, nil
}

func containsInt(list []int, v int) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
