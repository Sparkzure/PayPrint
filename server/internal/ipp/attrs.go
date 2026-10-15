package ipp

import (
	"fmt"

	"github.com/OpenPrinting/goipp"

	"github.com/Sparkzure/PayPrint/server/internal/domain"
)

const (
	jobStatePending           = 3
	jobStatePendingHeld       = 4
	jobStateProcessing        = 5
	jobStateProcessingStopped = 6
	jobStateCanceled          = 7
	jobStateAborted           = 8
	jobStateCompleted         = 9
)

const (
	JobStatePending           = jobStatePending
	JobStatePendingHeld       = jobStatePendingHeld
	JobStateProcessing        = jobStateProcessing
	JobStateProcessingStopped = jobStateProcessingStopped
	JobStateCanceled          = jobStateCanceled
	JobStateAborted           = jobStateAborted
	JobStateCompleted         = jobStateCompleted
)

const (
	PrinterStateIdle       = 3
	PrinterStateProcessing = 4
	PrinterStateStopped    = 5
)

const (
	printerStateIdle       = PrinterStateIdle
	printerStateProcessing = PrinterStateProcessing
	printerStateStopped    = PrinterStateStopped
)

const (
	orientationPortrait  = 3
	orientationLandscape = 4
)

const (
	qualityDraft  = 3
	qualityNormal = 4
	qualityHigh   = 5
)

func ApplyParams(job *goipp.Attributes, p domain.PrintParams, ranges []domain.PageRange) {
	params := p.Normalize()

	job.Add(goipp.MakeAttribute("copies", goipp.TagInteger, goipp.Integer(params.Copies)))

	sides := "one-sided"
	switch params.Duplex {
	case domain.DuplexLongEdge:
		sides = "two-sided-long-edge"
	case domain.DuplexShortEdge:
		sides = "two-sided-short-edge"
	}
	job.Add(goipp.MakeAttribute("sides", goipp.TagKeyword, goipp.String(sides)))

	color := "monochrome"
	if params.ColorMode == domain.ColorModeColor {
		color = "color"
	}
	job.Add(goipp.MakeAttribute("print-color-mode", goipp.TagKeyword, goipp.String(color)))

	job.Add(goipp.MakeAttribute("media", goipp.TagKeyword, goipp.String(mediaKeyword(params.PaperSize))))

	job.Add(goipp.MakeAttribute("number-up", goipp.TagInteger, goipp.Integer(params.NUp)))

	scaling := "auto"
	if params.FitToPage {
		scaling = "fit"
	}
	job.Add(goipp.MakeAttribute("print-scaling", goipp.TagKeyword, goipp.String(scaling)))

	orientation := orientationPortrait
	if params.Orientation == domain.OrientationLandscape {
		orientation = orientationLandscape
	}
	job.Add(goipp.MakeAttribute("orientation-requested", goipp.TagEnum, goipp.Integer(orientation)))

	job.Add(goipp.MakeAttribute("print-quality", goipp.TagEnum, goipp.Integer(qualityCode(params.Quality))))

	if len(ranges) > 0 {
		vals := make([]goipp.Value, 0, len(ranges))
		for _, r := range ranges {
			vals = append(vals, goipp.Range{Lower: r.From, Upper: r.To})
		}
		job.Add(goipp.MakeAttr("page-ranges", goipp.TagRange, vals[0], vals[1:]...))
	}
}

func qualityCode(q domain.PrintQuality) int {
	switch q {
	case domain.QualityDraft:
		return qualityDraft
	case domain.QualityHigh:
		return qualityHigh
	default:
		return qualityNormal
	}
}

func mediaKeyword(size domain.PaperSize) string {
	switch size {
	case domain.PaperA5:
		return "iso_a5_148x210mm"
	case domain.PaperA3:
		return "iso_a3_300x420mm"
	case domain.PaperLetter:
		return "na_letter_8.5x11in"
	default:
		return "iso_a4_210x297mm"
	}
}

func JobStateKeyword(state int64) string {
	switch state {
	case jobStatePending:
		return "pending"
	case jobStatePendingHeld:
		return "pending-held"
	case jobStateProcessing:
		return "processing"
	case jobStateProcessingStopped:
		return "processing-stopped"
	case jobStateCanceled:
		return "canceled"
	case jobStateAborted:
		return "aborted"
	case jobStateCompleted:
		return "completed"
	default:
		return fmt.Sprintf("state-%d", state)
	}
}

func JobStateCode(keyword string) int64 {
	switch keyword {
	case "pending":
		return jobStatePending
	case "pending-held":
		return jobStatePendingHeld
	case "processing":
		return jobStateProcessing
	case "processing-stopped":
		return jobStateProcessingStopped
	case "canceled":
		return jobStateCanceled
	case "aborted":
		return jobStateAborted
	case "completed":
		return jobStateCompleted
	default:
		return 0
	}
}

func ResolveJobStatus(state int64) (domain.JobStatus, bool) {
	switch state {
	case jobStateCompleted:
		return domain.JobCompleted, true
	case jobStateAborted, jobStateCanceled:
		return domain.JobFailed, true
	case jobStateProcessingStopped:

		return domain.JobInterrupted, false
	case jobStateProcessing:
		return domain.JobPrinting, false
	default:

		return domain.JobSubmitted, false
	}
}

func attrInt(attrs goipp.Attributes, name string) (int64, bool) {
	for _, attr := range attrs {
		if attr.Name != name || len(attr.Values) == 0 {
			continue
		}
		if v, ok := attr.Values[0].V.(goipp.Integer); ok {
			return int64(v), true
		}
	}
	return 0, false
}

func attrString(attrs goipp.Attributes, name string) string {
	for _, attr := range attrs {
		if attr.Name != name || len(attr.Values) == 0 {
			continue
		}
		switch v := attr.Values[0].V.(type) {
		case goipp.String:
			return string(v)
		case goipp.TextWithLang:
			return v.Text
		}
	}
	return ""
}

func attrStrings(attrs goipp.Attributes, name string) []string {
	for _, attr := range attrs {
		if attr.Name != name {
			continue
		}
		out := make([]string, 0, len(attr.Values))
		for _, val := range attr.Values {
			switch v := val.V.(type) {
			case goipp.String:
				out = append(out, string(v))
			case goipp.TextWithLang:
				out = append(out, v.Text)
			}
		}
		return out
	}
	return nil
}

func jobStateKeyword(attrs goipp.Attributes) string {
	if state, ok := attrInt(attrs, "job-state"); ok {
		return JobStateKeyword(state)
	}

	return attrString(attrs, "job-state")
}

func printerStateKeyword(attrs goipp.Attributes) string {
	state, ok := attrInt(attrs, "printer-state")
	if !ok {
		return attrString(attrs, "printer-state")
	}
	switch state {
	case printerStateIdle:
		return "idle"
	case printerStateProcessing:
		return "processing"
	case printerStateStopped:
		return "stopped"
	default:
		return fmt.Sprintf("state-%d", state)
	}
}

func PrinterStateCode(keyword string) int64 {
	switch keyword {
	case "idle":
		return printerStateIdle
	case "processing":
		return printerStateProcessing
	case "stopped":
		return printerStateStopped
	default:
		return 0
	}
}

func attrBool(attrs goipp.Attributes, name string) (bool, bool) {
	for _, attr := range attrs {
		if attr.Name != name || len(attr.Values) == 0 {
			continue
		}
		if v, ok := attr.Values[0].V.(goipp.Boolean); ok {
			return bool(v), true
		}
	}
	return false, false
}
