package ktimport

import "testing"

func TestBotProtectionStreamName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"Bot Protection", true},
		{"  bot protection  ", true},
		{"Bot Protection -x", true},
		{"Bot Protection -❄️⚡️✅", true},
		{"Bot Protectionist", false},
		{"Замыкающий", true},
		{"  замыкающий  ", true},
		{"Замыкающий -x", true},
		{"Замыкающийпоток", false},
		{"white page", false},
		{"Not Bot Protection", false},
	}
	for _, tc := range cases {
		if got := BotProtectionStreamName(tc.name); got != tc.want {
			t.Fatalf("%q: got %v want %v", tc.name, got, tc.want)
		}
	}
}
