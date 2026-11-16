package store

import "testing"

func TestParseRedisMemory(t *testing.T) {
	info := "# Memory\r\nused_memory:429496730\r\nused_memory_human:409.60M\r\nmaxmemory:536870912\r\n" +
		"# Stats\r\nrejected_connections:2\r\n# Errorstats\r\nerrorstat_ERR:count=5\r\nerrorstat_OOM:count=7\r\n"
	m, err := ParseRedisMemory(info)
	if err != nil {
		t.Fatal(err)
	}
	want := RedisMemory{Used: 429496730, Max: 536870912, OOMErrors: 7, Rejected: 2}
	if m != want {
		t.Fatalf("got %+v, want %+v", m, want)
	}
	m, err = ParseRedisMemory("used_memory:100\nmaxmemory:0\n")
	if err != nil || m.Max != 0 || m.Used != 100 {
		t.Fatalf("unlimited: %+v %v", m, err)
	}
	if _, err := ParseRedisMemory("-ERR unknown"); err == nil {
		t.Fatal("text without used_memory must fail")
	}
}
