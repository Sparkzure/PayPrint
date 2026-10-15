package ipp

import (
	"testing"

	"github.com/OpenPrinting/goipp"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
)

func mustInt(t *testing.T, attrs goipp.Attributes, name string) int64 {
	t.Helper()
	v, ok := attrInt(attrs, name)
	if !ok {
		t.Fatalf("缺少整数属性 %s", name)
	}
	return v
}

func TestApplyParamsMapsEveryField(t *testing.T) {
	var job goipp.Attributes
	ApplyParams(&job, domain.PrintParams{
		Copies:      3,
		Duplex:      domain.DuplexLongEdge,
		ColorMode:   domain.ColorModeColor,
		PaperSize:   domain.PaperA3,
		NUp:         2,
		Orientation: domain.OrientationLandscape,
		Quality:     domain.QualityHigh,
		FitToPage:   true,
	}, []domain.PageRange{{From: 2, To: 5}})

	keywords := []struct {
		name string
		got  string
		want string
	}{
		{"sides", attrString(job, "sides"), "two-sided-long-edge"},
		{"print-color-mode", attrString(job, "print-color-mode"), "color"},
		{"media", attrString(job, "media"), "iso_a3_300x420mm"},
		{"print-scaling", attrString(job, "print-scaling"), "fit"},
	}
	for _, c := range keywords {
		if c.got != c.want {
			t.Errorf("%s = %q，期望 %q", c.name, c.got, c.want)
		}
	}

	numbers := []struct {
		name string
		got  int64
		want int64
	}{
		{"copies", mustInt(t, job, "copies"), 3},
		{"number-up", mustInt(t, job, "number-up"), 2},
		{"orientation-requested", mustInt(t, job, "orientation-requested"), 4},
		{"print-quality", mustInt(t, job, "print-quality"), 5},
	}
	for _, c := range numbers {
		if c.got != c.want {
			t.Errorf("%s = %d，期望 %d", c.name, c.got, c.want)
		}
	}

	var rangeAttr *goipp.Attribute
	for i := range job {
		if job[i].Name == "page-ranges" {
			rangeAttr = &job[i]
		}
	}
	if rangeAttr == nil {
		t.Fatal("缺少 page-ranges 属性")
	}
	r, ok := rangeAttr.Values[0].V.(goipp.Range)
	if !ok {
		t.Fatalf("page-ranges 值类型应为 Range，实际 %T", rangeAttr.Values[0].V)
	}
	if r.Lower != 2 || r.Upper != 5 {
		t.Fatalf("page-ranges = %v，期望 2-5", r)
	}
}

func TestApplyParamsDefaults(t *testing.T) {
	var job goipp.Attributes
	ApplyParams(&job, domain.PrintParams{}, nil)

	if got := attrString(job, "sides"); got != "one-sided" {
		t.Errorf("默认应为单面，实际 %q", got)
	}
	if got := attrString(job, "print-color-mode"); got != "monochrome" {
		t.Errorf("默认应为黑白，实际 %q", got)
	}
	if got := attrString(job, "media"); got != "iso_a4_210x297mm" {
		t.Errorf("默认应为 A4，实际 %q", got)
	}
	for _, attr := range job {
		if attr.Name == "page-ranges" {
			t.Error("未指定页范围时不应下发 page-ranges")
		}
	}
}

func TestTransportURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"ipp://192.168.1.50/printers/office", "http://192.168.1.50:631/printers/office"},
		{"ipps://printer.local:8631/ipp/print", "https://printer.local:8631/ipp/print"},
		{"ipp://192.168.1.50", "http://192.168.1.50:631/ipp/print"},
		{"http://10.0.0.5:631/ipp/print", "http://10.0.0.5:631/ipp/print"},
	}
	for _, c := range cases {
		got, err := transportURL(c.in)
		if err != nil {
			t.Errorf("transportURL(%q) 返回错误: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("transportURL(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}

	for _, bad := range []string{"ftp://host/q", "", "not-a-url", "ipp://"} {
		if _, err := transportURL(bad); err == nil {
			t.Errorf("transportURL(%q) 应当报错", bad)
		}
	}
}

func TestResolveJobStatus(t *testing.T) {
	cases := []struct {
		state      int64
		wantStatus domain.JobStatus
		wantDone   bool
	}{
		{jobStatePending, domain.JobSubmitted, false},
		{jobStatePendingHeld, domain.JobSubmitted, false},
		{jobStateProcessing, domain.JobPrinting, false},

		{jobStateProcessingStopped, domain.JobInterrupted, false},
		{jobStateCompleted, domain.JobCompleted, true},
		{jobStateAborted, domain.JobFailed, true},
		{jobStateCanceled, domain.JobFailed, true},
	}
	for _, c := range cases {
		status, done := ResolveJobStatus(c.state)
		if status != c.wantStatus || done != c.wantDone {
			t.Errorf("ResolveJobStatus(%d) = (%s, %v)，期望 (%s, %v)",
				c.state, status, done, c.wantStatus, c.wantDone)
		}
	}
}

func TestJobStateKeywordRoundTrip(t *testing.T) {
	for _, state := range []int64{
		jobStatePending, jobStatePendingHeld, jobStateProcessing,
		jobStateProcessingStopped, jobStateCanceled, jobStateAborted, jobStateCompleted,
	} {
		keyword := JobStateKeyword(state)
		if back := JobStateCode(keyword); back != state {
			t.Errorf("关键字往返失败: %d -> %q -> %d", state, keyword, back)
		}
	}
	if code := JobStateCode("完全不认识的状态"); code != 0 {
		t.Errorf("未知关键字应返回 0，实际 %d", code)
	}
}
