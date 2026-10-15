package pdfutil

import (
	"bytes"
	"errors"
	"io"
)

var ErrUnknown = errors.New("无法识别 PDF 页数")

const (
	readChunk   = 64 * 1024
	maxScanSize = 32 << 20
)

func CountPages(r io.Reader) (int, error) {
	data, err := readLimited(r, maxScanSize)
	if err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, ErrUnknown
	}

	positions := indexAll(data, []byte("/Type"))
	if len(positions) == 0 {
		return 0, ErrUnknown
	}

	pages := 0
	for _, idx := range positions {
		if startsWithPage(data[idx+len("/Type"):]) {
			pages++
		}
	}
	if pages == 0 {
		return 0, ErrUnknown
	}
	return pages, nil
}

func startsWithPage(rest []byte) bool {
	i := skipWhitespace(rest)
	const pageName = "/Page"
	if len(rest)-i < len(pageName) {
		return false
	}
	if !bytes.Equal(rest[i:i+len(pageName)], []byte(pageName)) {
		return false
	}

	next := i + len(pageName)
	if next < len(rest) && isNameChar(rest[next]) {
		return false
	}
	return true
}

func isNameChar(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z':
		return true
	case b >= 'A' && b <= 'Z':
		return true
	case b >= '0' && b <= '9':
		return true
	case b == '_' || b == '-' || b == '+':
		return true
	}
	return false
}

func skipWhitespace(b []byte) int {
	i := 0
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\r', '\n', '\f', '\v', 0x00:
			i++
			continue
		}
		break
	}
	return i
}

func indexAll(data, needle []byte) []int {
	var out []int
	offset := 0
	for {
		idx := bytes.Index(data[offset:], needle)
		if idx < 0 {
			return out
		}
		out = append(out, offset+idx)
		offset += idx + len(needle)
		if offset >= len(data) {
			return out
		}
	}
}

func readLimited(r io.Reader, limit int) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(readChunk)

	chunk := make([]byte, readChunk)
	for buf.Len() < limit {
		n, err := r.Read(chunk)
		if n > 0 {
			remaining := limit - buf.Len()
			if n > remaining {
				n = remaining
			}
			buf.Write(chunk[:n])
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if n == 0 {
			break
		}
	}
	return buf.Bytes(), nil
}

func LooksLikePDF(data []byte) bool {
	return bytes.HasPrefix(bytes.TrimLeft(data, "\x00\xef\xbb\xbf"), []byte("%PDF-"))
}
