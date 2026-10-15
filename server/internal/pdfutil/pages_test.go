package pdfutil

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

const samplePDF = `%PDF-1.4
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R >>
endobj
4 0 obj
<</Type/Page/Parent 2 0 R>>
endobj
5 0 obj
<< /Type /Page /Parent 2 0 R >>
endobj
trailer
<< /Root 1 0 R >>
%%EOF`

func TestCountPages(t *testing.T) {
	pages, err := CountPages(strings.NewReader(samplePDF))
	if err != nil {
		t.Fatalf("CountPages 返回错误: %v", err)
	}
	if pages != 3 {
		t.Fatalf("页数 = %d，期望 3（/Type /Pages 不应被计入）", pages)
	}
}

func TestCountPagesHandlesNoSpaceVariant(t *testing.T) {
	pdf := `%PDF-1.4
<</Type/Catalog/Pages 2 0 R>>
<</Type/Pages/Kids[3 0 R] /Count 1>>
<</Type/Page/Parent 2 0 R>>
%%EOF`
	pages, err := CountPages(strings.NewReader(pdf))
	if err != nil {
		t.Fatalf("CountPages 返回错误: %v", err)
	}
	if pages != 1 {
		t.Fatalf("页数 = %d，期望 1", pages)
	}
}

func TestCountPagesUnknown(t *testing.T) {
	if _, err := CountPages(strings.NewReader("%PDF-1.4\nno page objects here")); !errors.Is(err, ErrUnknown) {
		t.Fatalf("无法识别时应返回 ErrUnknown，实际 %v", err)
	}
	if _, err := CountPages(bytes.NewReader(nil)); !errors.Is(err, ErrUnknown) {
		t.Fatalf("空内容应返回 ErrUnknown，实际 %v", err)
	}
}

func TestLooksLikePDF(t *testing.T) {
	if !LooksLikePDF([]byte("%PDF-1.7\n")) {
		t.Fatal("标准 PDF 头应当通过")
	}
	if LooksLikePDF([]byte("<html>")) {
		t.Fatal("HTML 不应被当作 PDF")
	}
}
