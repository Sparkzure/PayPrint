package pdfutil

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
)

const maxPDFImagePixels = 100_000_000

const (
	a4ShortSide = 595.28
	a4LongSide  = 841.89
)

func IsImage(data []byte) bool {
	return looksLikeJPEG(data) || looksLikePNG(data)
}

func looksLikeJPEG(data []byte) bool {
	return len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF
}

func looksLikePNG(data []byte) bool {
	return len(data) >= 8 &&
		data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 &&
		data[4] == 0x0D && data[5] == 0x0A && data[6] == 0x1A && data[7] == 0x0A
}

func ImageToPDF(data []byte) ([]byte, error) {
	switch {
	case looksLikeJPEG(data):
		return jpegToPDF(data)
	case looksLikePNG(data):
		return pngToPDF(data)
	default:
		return nil, errors.New("不是受支持的图片格式（仅 JPG / PNG）")
	}
}

func jpegToPDF(data []byte) ([]byte, error) {
	width, height, comps, err := jpegDimensions(data)
	if err == nil && (comps == 1 || comps == 3) {
		if err := checkPixels(width, height); err != nil {
			return nil, err
		}
		space := "DeviceGray"
		if comps == 3 {
			space = "DeviceRGB"
		}
		return buildPDF(width, height, space, "DCTDecode", data)
	}

	img, derr := jpeg.Decode(bytes.NewReader(data))
	if derr != nil {
		return nil, fmt.Errorf("解析 JPEG 失败: %w", derr)
	}
	return rasterToPDF(img)
}

func pngToPDF(data []byte) ([]byte, error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("解析 PNG 失败: %w", err)
	}
	return rasterToPDF(img)
}

func rasterToPDF(img image.Image) ([]byte, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, errors.New("图片尺寸非法")
	}
	if err := checkPixels(w, h); err != nil {
		return nil, err
	}

	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(canvas, canvas.Bounds(), img, b.Min, draw.Over)

	raw := make([]byte, 0, w*h*3)
	for y := 0; y < h; y++ {
		rowStart := y * canvas.Stride
		for x := 0; x < w; x++ {
			i := rowStart + x*4
			raw = append(raw, canvas.Pix[i], canvas.Pix[i+1], canvas.Pix[i+2])
		}
	}

	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	if _, err := zw.Write(raw); err != nil {
		return nil, fmt.Errorf("压缩像素数据: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("压缩像素数据: %w", err)
	}
	return buildPDF(w, h, "DeviceRGB", "FlateDecode", compressed.Bytes())
}

func checkPixels(w, h int) error {
	if int64(w)*int64(h) > maxPDFImagePixels {
		return fmt.Errorf("图片像素过多（%d x %d），请先压缩后再上传", w, h)
	}
	return nil
}

func jpegDimensions(data []byte) (width, height, comps int, err error) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 0, 0, 0, errors.New("不是 JPEG 位流")
	}
	i := 2
	for i+1 < len(data) {
		if data[i] != 0xFF {
			i++
			continue
		}
		marker := data[i+1]
		switch {
		case marker == 0xFF:
			i++
			continue
		case marker == 0xD8 || marker == 0xD9 || marker == 0x01 ||
			(marker >= 0xD0 && marker <= 0xD7):
			i += 2
			continue
		}
		if i+4 > len(data) {
			break
		}
		segLen := int(data[i+2])<<8 | int(data[i+3])
		if segLen < 2 {
			return 0, 0, 0, errors.New("JPEG 段长度非法")
		}

		if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			if segLen < 7 || i+2+segLen > len(data) {
				return 0, 0, 0, errors.New("JPEG SOF 段不完整")
			}
			height = int(data[i+5])<<8 | int(data[i+6])
			width = int(data[i+7])<<8 | int(data[i+8])
			comps = int(data[i+9])
			if width <= 0 || height <= 0 {
				return 0, 0, 0, errors.New("JPEG 尺寸非法")
			}
			return width, height, comps, nil
		}
		i += 2 + segLen
	}
	return 0, 0, 0, errors.New("未找到 JPEG 图像尺寸标记")
}

func buildPDF(imgW, imgH int, colorSpace, filter string, imgData []byte) ([]byte, error) {
	var buf bytes.Buffer

	buf.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, 6)

	writeObj := func(id int, dict string) {
		offsets[id] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", id, dict)
	}

	pageW, pageH := pageBoxFor(imgW, imgH)
	drawW, drawH, offsetX, offsetY := fitWithin(imgW, imgH, pageW, pageH)

	writeObj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	writeObj(3, fmt.Sprintf(
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] "+
			"/Resources << /XObject << /Im0 4 0 R >> >> /Contents 5 0 R >>",
		pageW, pageH))

	offsets[4] = buf.Len()
	fmt.Fprintf(&buf,
		"4 0 obj\n<< /Type /XObject /Subtype /Image /Width %d /Height %d "+
			"/ColorSpace /%s /BitsPerComponent 8 /Filter /%s /Length %d >>\nstream\n",
		imgW, imgH, colorSpace, filter, len(imgData))
	buf.Write(imgData)
	buf.WriteString("\nendstream\nendobj\n")

	content := fmt.Sprintf("q\n%.2f 0 0 %.2f %.2f %.2f cm\n/Im0 Do\nQ\n",
		drawW, drawH, offsetX, offsetY)
	offsets[5] = buf.Len()
	fmt.Fprintf(&buf, "5 0 obj\n<< /Length %d >>\nstream\n%s endstream\nendobj\n",
		len(content), content)

	xrefPos := buf.Len()
	buf.WriteString("xref\n0 6\n")
	buf.WriteString("0000000000 65535 f \n")
	for id := 1; id <= 5; id++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[id])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefPos)

	return buf.Bytes(), nil
}

func pageBoxFor(imgW, imgH int) (w, h float64) {
	if imgW >= imgH {
		return a4LongSide, a4ShortSide
	}
	return a4ShortSide, a4LongSide
}

func fitWithin(srcW, srcH int, boxW, boxH float64) (w, h, x, y float64) {
	scale := math.Min(boxW/float64(srcW), boxH/float64(srcH))
	w = float64(srcW) * scale
	h = float64(srcH) * scale
	return w, h, (boxW - w) / 2, (boxH - h) / 2
}
