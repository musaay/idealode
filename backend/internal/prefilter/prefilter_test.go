package prefilter

import "testing"

func TestShouldAnalyze(t *testing.T) {
	cases := []struct {
		platform, title, body string
		want                  bool
	}{
		{"hackernews", "Ask HN", "I WISH there was a tool for this", true}, // case-insensitive
		{"hackernews", "Is there a tool for syncing?", "", true},           // title'da sinyal
		{"hackernews", "Show HN: my new game", "just a fun project", false},
		{"hackernews", "", "", false},
		{"stackexchange", "Random title", "no signal words at all", true}, // bypass: doğal filtreli
	}
	for _, c := range cases {
		if got := ShouldAnalyze(c.platform, c.title, c.body); got != c.want {
			t.Errorf("ShouldAnalyze(%q, %q, %q) = %v, beklenen %v", c.platform, c.title, c.body, got, c.want)
		}
	}
}
