package domain

import "testing"

func TestPageRangeExpansionIsBounded(t *testing.T) {

	if _, err := ParsePageRange("1-2147483647", 3); err == nil {
		t.Fatal("超出上界的页范围应当被拒绝")
	}

	if _, err := ParsePageRange("2147483647", MaxPageCount+1); err == nil {
		t.Fatal("超出上界的单页应当被拒绝")
	}

	if _, err := ParsePageRange("1-100000", 200000); err == nil {
		t.Fatal("页范围应当受业务上界约束，而不是跟着文件页数走")
	}

	ranges, err := ParsePageRange("1-5000", 10)
	if err != nil {
		t.Fatalf("上限内的页范围应当接受: %v", err)
	}
	if Total(ranges) != 10 {
		t.Fatalf("超出文件页数的部分应被裁剪，实际 %d 页", Total(ranges))
	}

	all, err := ParsePageRange("", 9000)
	if err != nil {
		t.Fatalf("整篇打印应当仍然可用: %v", err)
	}
	if Total(all) != 9000 {
		t.Fatalf("整篇应当是 9000 页，实际 %d", Total(all))
	}
}
