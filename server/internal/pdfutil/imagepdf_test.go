package pdfutil

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"testing"
)

const floatTolerance = 0.01

func sampleImage() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 6, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 6; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 40), G: uint8(y * 60), B: 128, A: 255})
		}
	}
	return img
}

func TestImageToPDFPNG(t *testing.T) {
	var raw bytes.Buffer
	if err := png.Encode(&raw, sampleImage()); err != nil {
		t.Fatalf("编码 PNG: %v", err)
	}
	if !IsImage(raw.Bytes()) {
		t.Fatal("PNG 应被识别为图片")
	}

	pdf, err := ImageToPDF(raw.Bytes())
	if err != nil {
		t.Fatalf("转换 PNG: %v", err)
	}
	if !LooksLikePDF(pdf) {
		t.Fatal("转换结果应当是 PDF")
	}
	pages, err := CountPages(bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("统计页数: %v", err)
	}
	if pages != 1 {
		t.Fatalf("页数 = %d，期望 1", pages)
	}
}

func TestImageToPDFJPEGEmbedsBitstream(t *testing.T) {
	var raw bytes.Buffer
	if err := jpeg.Encode(&raw, sampleImage(), &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("编码 JPEG: %v", err)
	}
	if !IsImage(raw.Bytes()) {
		t.Fatal("JPEG 应被识别为图片")
	}

	w, h, comps, err := jpegDimensions(raw.Bytes())
	if err != nil {
		t.Fatalf("读取 JPEG 尺寸: %v", err)
	}
	if w != 6 || h != 4 {
		t.Fatalf("JPEG 尺寸 = %dx%d，期望 6x4", w, h)
	}
	if comps != 3 {
		t.Fatalf("JPEG 分量数 = %d，期望 3", comps)
	}

	pdf, err := ImageToPDF(raw.Bytes())
	if err != nil {
		t.Fatalf("转换 JPEG: %v", err)
	}
	if !bytes.Contains(pdf, []byte("/DCTDecode")) {
		t.Fatal("三分量 JPEG 应以 DCTDecode 原样嵌入")
	}
	pages, err := CountPages(bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("统计页数: %v", err)
	}
	if pages != 1 {
		t.Fatalf("页数 = %d，期望 1", pages)
	}
}

func TestImageToPDFRejectsGarbage(t *testing.T) {
	if _, err := ImageToPDF([]byte("not an image at all")); err == nil {
		t.Fatal("非图片内容应当报错")
	}
	if IsImage([]byte("%PDF-1.7")) {
		t.Fatal("PDF 不应被识别为图片")
	}
}

func TestPageBoxForFollowsImageOrientation(t *testing.T) {
	cases := []struct {
		name    string
		w, h    int
		wantW   float64
		wantH   float64
		wantWhy string
	}{
		{"竖图 -> 纵向 A4", 600, 900, a4ShortSide, a4LongSide, ""},
		{"横图 -> 横向 A4", 900, 600, a4LongSide, a4ShortSide, ""},
		{"正方形按横向处理", 500, 500, a4LongSide, a4ShortSide, ""},
	}
	for _, tc := range cases {
		gotW, gotH := pageBoxFor(tc.w, tc.h)
		if math.Abs(gotW-tc.wantW) > floatTolerance || math.Abs(gotH-tc.wantH) > floatTolerance {
			t.Fatalf("%s: 页面 = %.2fx%.2f，期望 %.2fx%.2f %s",
				tc.name, gotW, gotH, tc.wantW, tc.wantH, tc.wantWhy)
		}
	}
}

func TestFitWithinPreservesAspectAndCenters(t *testing.T) {

	w, h, x, y := fitWithin(600, 800, a4ShortSide, a4LongSide)
	if math.Abs(w-a4ShortSide) > floatTolerance {
		t.Fatalf("宽度应铺满 A4 短边，实际 %.2f", w)
	}
	if math.Abs(w/h-600.0/800.0) > 1e-6 {
		t.Fatalf("宽高比被改变了：%.4f", w/h)
	}
	if math.Abs(x) > floatTolerance {
		t.Fatalf("宽度到边界时不应有水平留白，实际 x=%.2f", x)
	}
	if y <= 0 || math.Abs((a4LongSide-h)/2-y) > floatTolerance {
		t.Fatalf("垂直方向应居中：y=%.2f h=%.2f", y, h)
	}

	w, h, x, y = fitWithin(800, 600, a4LongSide, a4ShortSide)
	if math.Abs(h-a4ShortSide) > floatTolerance {
		t.Fatalf("高度应铺满 A4 短边，实际 %.2f", h)
	}
	if math.Abs(y) > floatTolerance {
		t.Fatalf("高度到边界时不应有垂直留白，实际 y=%.2f", y)
	}
	if x <= 0 || math.Abs((a4LongSide-w)/2-x) > floatTolerance {
		t.Fatalf("水平方向应居中：x=%.2f w=%.2f", x, w)
	}
}

func TestImageToPDFUsesFixedA4PageBox(t *testing.T) {
	var raw bytes.Buffer
	if err := png.Encode(&raw, sampleImage()); err != nil {
		t.Fatalf("编码 PNG: %v", err)
	}
	pdf, err := ImageToPDF(raw.Bytes())
	if err != nil {
		t.Fatalf("转换 PNG: %v", err)
	}

	if !bytes.Contains(pdf, []byte("/MediaBox [0 0 841.89 595.28]")) {
		t.Fatalf("页面盒应当是横向 A4，实际 PDF 开头：\n%s", pdf[:min(400, len(pdf))])
	}

	if bytes.Contains(pdf, []byte("6.00 0 0 4.00 0.00 0.00 cm")) {
		t.Fatal("图片仍按像素尺寸铺满页面，缩放没有生效")
	}
}
