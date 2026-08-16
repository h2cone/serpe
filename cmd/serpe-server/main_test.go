package main

import (
	"testing"

	"github.com/h2cone/serpe/core/tools"
)

func TestParseToolNames(t *testing.T) {
	t.Parallel()
	if _, err := parseToolNames("read,"); err == nil {
		t.Fatal("trailing comma succeeded")
	}
	if _, err := parseToolNames(" read"); err == nil {
		t.Fatal("padded name succeeded")
	}
	if _, err := parseToolNames("none,read"); err == nil {
		t.Fatal("mixed none succeeded")
	}
	names, err := parseToolNames("read,write")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "read" || names[1] != "write" {
		t.Fatalf("names=%v", names)
	}
}

func TestResolveLocalToolsContract(t *testing.T) {
	none, err := resolveLocalTools("none")
	if err != nil {
		t.Fatal(err)
	}
	if none != nil {
		t.Fatalf("none=%v", none)
	}
	if _, err := resolveLocalTools("read,"); err == nil {
		t.Fatal("invalid list succeeded")
	}

	all, err := resolveLocalTools("")
	if err != nil {
		t.Fatal(err)
	}
	if got := toolNames(all); len(got) != 4 || got[0] != "read" || got[3] != "bash" {
		t.Fatalf("default tools=%v", got)
	}

	read, err := resolveLocalTools("read")
	if err != nil {
		t.Fatal(err)
	}
	if got := toolNames(read); len(got) != 1 || got[0] != "read" {
		t.Fatalf("read=%v", got)
	}
}

func toolNames(list []tools.Tool) []string {
	names := make([]string, 0, len(list))
	for _, tool := range list {
		names = append(names, tool.Definition().Name)
	}
	return names
}
