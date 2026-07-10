package main

import (
	"strings"
	"testing"
)

func TestParseCLI(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    cliArgs
		wantErr string
	}{
		{
			name:    "missing model",
			wantErr: "missing required positional argument: model",
		},
		{
			name:    "missing task",
			args:    []string{"gpt-4.1-mini"},
			wantErr: "missing required positional argument: task",
		},
		{
			name: "task joins remaining arguments",
			args: []string{"gpt-4.1-mini", "say", "hi"},
			want: cliArgs{Model: "gpt-4.1-mini", Task: "say hi"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseCLI(test.args)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("parseCLI() error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("parseCLI() = %#v, want %#v", got, test.want)
			}
		})
	}
}
