package service

import (
	"testing"
	"unicode/utf8"
)

func TestTruncateKeepsValidUTF8(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		limit int
		want  string
	}{
		{"不超上限时原样返回", "abc", 10, "abc"},
		{"恰好等于上限", "abc", 3, "abc"},
		{"ASCII 截断", "abcdef", 3, "abc"},
		{"中文按字符截断", "打印失败原因很长", 4, "打印失败"},
		{"中文边界不切半个汉字", "中", 1, "中"},
		{"四字节字符按一个字符算", "𠀀𠀀", 1, "𠀀"},
		{"上限为零返回空串", "abc", 0, ""},
		{"空串", "", 5, ""},
	}

	for _, c := range cases {
		got := truncate(c.in, c.limit)
		if got != c.want {
			t.Errorf("%s: truncate(%q, %d) = %q，期望 %q",
				c.name, c.in, c.limit, got, c.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("%s: 结果不是合法 UTF-8: %q", c.name, got)
		}
	}
}
