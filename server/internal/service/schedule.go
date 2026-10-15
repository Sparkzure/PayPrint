package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/store"
)

type OnlineWindow struct {
	Days  []int  `json:"days"`
	Start string `json:"start"`
	End   string `json:"end"`
}

const maxOnlineWindows = 12

const AlwaysOnlineText = "全天在线"

var weekdayNames = [8]string{"", "周一", "周二", "周三", "周四", "周五", "周六", "周日"}

func ParseOnlineWindows(raw string) []OnlineWindow {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var decoded []OnlineWindow
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil
	}
	out := make([]OnlineWindow, 0, len(decoded))
	for _, w := range decoded {
		clean, ok := normalizeOnlineWindow(w)
		if !ok {
			continue
		}
		out = append(out, clean)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func EncodeOnlineWindows(wins []OnlineWindow) (string, error) {
	normalized := make([]OnlineWindow, 0, len(wins))
	for _, w := range wins {
		if clean, ok := normalizeOnlineWindow(w); ok {
			normalized = append(normalized, clean)
		}
	}
	if len(normalized) == 0 {
		return "[]", nil
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func ValidateOnlineWindows(wins []OnlineWindow) error {
	if len(wins) > maxOnlineWindows {
		return domain.ErrValidation.WithMessage(
			fmt.Sprintf("在线时段最多 %d 条", maxOnlineWindows))
	}
	for i, w := range wins {
		if len(w.Days) == 0 {
			return domain.ErrValidation.WithMessage(
				fmt.Sprintf("第 %d 条在线时段至少要选一天", i+1))
		}
		for _, d := range w.Days {
			if d < 1 || d > 7 {
				return domain.ErrValidation.WithMessage(
					fmt.Sprintf("第 %d 条在线时段的工作日只能取 1-7（周一为 1）", i+1))
			}
		}
		start, startOK := parseClock(w.Start)
		if !startOK {
			return domain.ErrValidation.WithMessage(
				fmt.Sprintf("第 %d 条在线时段的开始时间必须是 HH:MM", i+1))
		}
		end, endOK := parseClock(w.End)
		if !endOK {
			return domain.ErrValidation.WithMessage(
				fmt.Sprintf("第 %d 条在线时段的结束时间必须是 HH:MM", i+1))
		}
		if start == end {
			return domain.ErrValidation.WithMessage(
				fmt.Sprintf("第 %d 条在线时段的开始与结束时间不能相同；要全天在线就清空全部时段", i+1))
		}
	}
	return nil
}

func InOnlineWindow(wins []OnlineWindow, t time.Time) bool {
	if len(wins) == 0 {
		return true
	}
	minute := t.Hour()*60 + t.Minute()
	today := isoWeekday(t)
	for _, w := range wins {
		start, startOK := parseClock(w.Start)
		end, endOK := parseClock(w.End)
		if !startOK || !endOK || start == end {
			continue
		}
		if start < end {
			if containsInt(w.Days, today) && minute >= start && minute < end {
				return true
			}
			continue
		}

		if containsInt(w.Days, today) && minute >= start {
			return true
		}
		if containsInt(w.Days, prevWeekday(today)) && minute < end {
			return true
		}
	}
	return false
}

func NextOnlineStart(wins []OnlineWindow, t time.Time) time.Time {
	if len(wins) == 0 || InOnlineWindow(wins, t) {
		return t
	}
	base := t.Truncate(time.Minute)
	var best time.Time
	for _, w := range wins {
		start, ok := parseClock(w.Start)
		if !ok {
			continue
		}
		for _, d := range w.Days {
			if d < 1 || d > 7 {
				continue
			}
			cand := sameISOWeekday(base, d).Add(time.Duration(start) * time.Minute)
			if !cand.After(base) {
				cand = cand.AddDate(0, 0, 7)
			}
			if best.IsZero() || cand.Before(best) {
				best = cand
			}
		}
	}
	return best
}

func DescribeOnlineWindows(wins []OnlineWindow) string {
	if len(wins) == 0 {
		return AlwaysOnlineText
	}
	parts := make([]string, 0, len(wins))
	for _, w := range wins {
		start, startOK := parseClock(w.Start)
		end, endOK := parseClock(w.End)
		if !startOK || !endOK {
			continue
		}
		parts = append(parts, fmt.Sprintf(
			"%s %s-%s", describeDays(w.Days), formatClock(start), formatClock(end)))
	}
	if len(parts) == 0 {
		return AlwaysOnlineText
	}
	return strings.Join(parts, "、")
}

func (svc *PrinterService) EnsureOnline(p store.Printer) error {
	wins := ParseOnlineWindows(p.OnlineWindows)
	if InOnlineWindow(wins, svc.now()) {
		return nil
	}
	return domain.ErrPrinterClosed.WithMessage(fmt.Sprintf(
		"打印机「%s」当前不在在线时段（在线时段：%s）", p.Name, DescribeOnlineWindows(wins)))
}

func (svc *PrinterService) NextOpenAt(p store.Printer, now time.Time) time.Time {
	return NextOnlineStart(ParseOnlineWindows(p.OnlineWindows), now)
}

func parseClock(raw string) (int, bool) {
	parsed, err := time.Parse("15:04", strings.TrimSpace(raw))
	if err != nil {
		return 0, false
	}
	return parsed.Hour()*60 + parsed.Minute(), true
}

func formatClock(minute int) string {
	return fmt.Sprintf("%02d:%02d", minute/60, minute%60)
}

func normalizeOnlineWindow(w OnlineWindow) (OnlineWindow, bool) {
	seen := make(map[int]bool, len(w.Days))
	days := make([]int, 0, len(w.Days))
	for _, d := range w.Days {
		if d < 1 || d > 7 || seen[d] {
			continue
		}
		seen[d] = true
		days = append(days, d)
	}
	if len(days) == 0 {
		return OnlineWindow{}, false
	}
	sort.Ints(days)

	start, ok := parseClock(w.Start)
	if !ok {
		return OnlineWindow{}, false
	}
	end, ok := parseClock(w.End)
	if !ok || start == end {
		return OnlineWindow{}, false
	}
	return OnlineWindow{Days: days, Start: formatClock(start), End: formatClock(end)}, true
}

func describeDays(days []int) string {
	seen := make(map[int]bool, len(days))
	uniq := make([]int, 0, len(days))
	for _, d := range days {
		if d < 1 || d > 7 || seen[d] {
			continue
		}
		seen[d] = true
		uniq = append(uniq, d)
	}
	sort.Ints(uniq)
	switch len(uniq) {
	case 0:
		return ""
	case 7:
		return "每天"
	}

	var parts []string
	for i := 0; i < len(uniq); {
		j := i
		for j+1 < len(uniq) && uniq[j+1] == uniq[j]+1 {
			j++
		}
		switch {
		case j == i:
			parts = append(parts, weekdayNames[uniq[i]])
		case j == i+1:

			parts = append(parts, weekdayNames[uniq[i]], weekdayNames[uniq[j]])
		default:
			parts = append(parts, weekdayNames[uniq[i]]+"至"+weekdayNames[uniq[j]])
		}
		i = j + 1
	}
	return strings.Join(parts, "、")
}

func isoWeekday(t time.Time) int {
	if wd := int(t.Weekday()); wd != 0 {
		return wd
	}
	return 7
}

func prevWeekday(iso int) int {
	if iso <= 1 {
		return 7
	}
	return iso - 1
}

func sameISOWeekday(t time.Time, iso int) time.Time {
	day := t.AddDate(0, 0, iso-isoWeekday(t))
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
}

func containsInt(list []int, v int) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
