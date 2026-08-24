package perms

import "testing"

func TestIsDestructiveGitTable(t *testing.T) {
	cases := []struct {
		name string
		sub  string
		args []string
		want bool
	}{
		// push / force-push family.
		{name: "push long force", sub: "push", args: []string{"--force", "origin"}, want: true},
		{name: "push short force", sub: "push", args: []string{"origin", "-f"}, want: true},
		{name: "push cluster containing f", sub: "push", args: []string{"-ff"}, want: true},
		{name: "push mixed cluster -fv", sub: "push", args: []string{"-fv"}, want: true},
		{name: "push uppercase long flag caught", sub: "push", args: []string{"--FORCE"}, want: true},
		{name: "push force-with-lease NOT floored", sub: "push", args: []string{"--force-with-lease=main", "origin"}, want: false},
		{name: "plain push not floored", sub: "push", args: []string{"origin", "main"}, want: false},
		{name: "PUSH subcommand casing still floored", sub: "PUSH", args: []string{"-f"}, want: true},

		// reset family.
		{name: "reset hard", sub: "reset", args: []string{"--hard"}, want: true},
		{name: "reset hard late position", sub: "reset", args: []string{"HEAD~2", "--hard"}, want: true},
		{name: "reset soft allowed", sub: "reset", args: []string{"--soft", "HEAD~1"}, want: false},
		{name: "reset mixed allowed", sub: "reset", args: nil, want: false},

		// clean family: always floored in v0.
		{name: "clean bare", sub: "clean", want: true},
		{name: "clean dry-run still floored", sub: "clean", args: []string{"-n"}, want: true},
		{name: "clean force dirs floored", sub: "clean", args: []string{"-fdx"}, want: true},

		// branch deletion family.
		{name: "branch uppercase D floored", sub: "branch", args: []string{"-D", "x"}, want: true},
		{name: "branch long delete floored", sub: "branch", args: []string{"--delete", "x"}, want: true},
		{name: "branch long delete alone still floored (design: only short -d is exempt)", sub: "branch", args: []string{"--delete"}, want: true},
		{name: "branch lowercase d allowed", sub: "branch", args: []string{"-d", "merged"}, want: false},
		{name: "branch list allowed", sub: "branch", args: []string{"--list"}, want: false},

		// Non-floored subcommands.
		{name: "status never floored", sub: "status", want: false},
		{name: "filter-branch out of v0 floor scope (extensible later)", sub: "filter-branch", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsDestructiveGit(tc.sub, tc.args); got != tc.want {
				t.Errorf("IsDestructiveGit(%q, %v) = %v, want %v", tc.sub, tc.args, got, tc.want)
			}
		})
	}
}
