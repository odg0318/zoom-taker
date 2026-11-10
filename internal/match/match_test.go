package match

import (
	"math"
	"testing"
)

// Expectations are bit-for-bit measurements from Python 3 difflib with the
// pipeline's normalize (re.sub(r"[\s.,!?~'\"…]+", "", t)) — the Go port must
// not drift from what the verified-meeting bleed filter actually did.
func TestRatioParity(t *testing.T) {
	cases := []struct {
		a, b string
		want float64
	}{
		{"abcd", "abcde", 0.8888888888888888},
		{"", "", 1.0}, // Python _calculate_ratio guard
		{"안녕하세요 반갑습니다", "안녕하세요 반갑습니다", 1.0},                     // identical
		{"안녕하세요 반갑습니다.", "안녕하세요, 반갑습니다!", 1.0},                  // punctuation ignored
		{"오늘 회의 시작하겠습니다", "오늘 회의를 시작하겠습니다", 0.9565217391304348}, // 조사 삽입
		{"다음 주 목요일까지 보내주세요", "다음주 목요일까지 보내 주세요", 1.0},           // 띄어쓰기만 다름
		{"회의록 공유하겠습니다", "공유하겠습니다", 0.8235294117647058},          // 부분 문자열 (substring TODO case)
		{"launch date is next monday", "launch date is next monday!", 1.0},
		{"thank you everyone", "thank you everyone so much", 0.8421052631578947},
		{"hello world", "goodbye world", 0.5454545454545454}, // below 0.6 threshold
		{"네", "네네", 0.6666666666666666},                      // just above 0.6 threshold
	}
	for _, c := range cases {
		if got := Ratio(c.a, c.b); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("Ratio(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// Python's regex \s is Unicode-aware: NBSP is stripped too.
func TestRatioNBSP(t *testing.T) {
	if got := Ratio("a b", "ab"); got != 1.0 {
		t.Errorf("NBSP not stripped: Ratio = %v, want 1.0", got)
	}
}
