package database

import "testing"

func TestEscapeLikeTreatsWildcardsAsLiterals(t *testing.T) {
	// 反斜杠自身必须先转义；% 与 _ 各自加反斜杠前缀，其余字符原样保留。
	tests := []struct{ input, want string }{
		{input: "ACME", want: "ACME"},
		{input: "50%订单", want: `50\%订单`},
		{input: "a_b", want: `a\_b`},
		{input: `C:\temp`, want: `C:\\temp`},
		{input: `\%_`, want: `\\\%\_`},
		{input: "中文 空格", want: "中文 空格"},
	}
	for _, test := range tests {
		if got := EscapeLike(test.input); got != test.want {
			t.Fatalf("EscapeLike(%q)=%q want %q", test.input, got, test.want)
		}
	}
}

func TestLikeContainsWrapsEscapedValue(t *testing.T) {
	if got := LikeContains("100%"); got != `%100\%%` {
		t.Fatalf("LikeContains= %q", got)
	}
	if got := LikeContains(""); got != "%%" {
		t.Fatalf("LikeContains empty= %q", got)
	}
}
