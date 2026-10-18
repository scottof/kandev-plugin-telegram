package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseCommand(t *testing.T) {
	cases := []struct {
		text string
		want command
		ok   bool
	}{
		{"/new Fix the login page", command{Name: "new", Args: "Fix the login page"}, true},
		{"/new@kandev_bot Fix it", command{Name: "new", Args: "Fix it"}, true},
		{"/new@Kandev_Bot Fix it", command{Name: "new", Args: "Fix it"}, true},
		{"/new", command{Name: "new"}, true},
		{"/new   ", command{Name: "new"}, true},
		{"/new first line\nsecond line", command{Name: "new", Args: "first line\nsecond line"}, true},
		{"/new\nonly on the next line", command{Name: "new", Args: "only on the next line"}, true},
		{"/status", command{Name: "status"}, true},
		{"/STATUS", command{Name: "status"}, true},
		{"/start", command{Name: "start"}, true},
		{"/frobnicate now", command{Name: "frobnicate", Args: "now"}, true},
		{"/new@other_bot Fix it", command{}, false},
		{"hello there", command{}, false},
		{"", command{}, false},
		{"/", command{}, false},
		{" /status", command{Name: "status"}, true},
	}
	for _, tc := range cases {
		got, ok := parseCommand(tc.text, "kandev_bot")
		require.Equal(t, tc.ok, ok, tc.text)
		require.Equal(t, tc.want, got, tc.text)
	}
}
