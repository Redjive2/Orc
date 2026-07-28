package provision

import (
	"strings"

	"orc/common/user"
	"orc/orc/internal/store"
)

// The identity's own Claude configuration.
//
// Every session Orc starts runs with CLAUDE_CONFIG_DIR pointed at this directory,
// which is what makes an identity's memories and instructions *its own* rather
// than the machine's. Two files matter, and only one of them exists yet:
//
//   - CLAUDE.md, the identity's standing instructions, written here;
//   - settings.json, compiled from the identity's effective permissions, which is
//     milestone 3's job.
//
// settings.json is deliberately **not** written as an empty placeholder. A
// settings file that exists and permits everything is a claim that the permission
// model is in force when it is not, and the one thing Plan.md §7 insists on is
// that each layer says what it actually enforces.

// claudeConfig lays out an identity's configuration directory.
func (p Provisioner) claudeConfig(name user.Name) error {
	if err := p.store.MakeClaudeDir(name, store.MemoryDir); err != nil {
		return err
	}
	return p.store.WriteClaudeFile(name, "CLAUDE.md", []byte(instructions(name)))
}

// instructions is the identity's starting CLAUDE.md.
//
// It says who the agent is and where its tools are, and nothing about how to do
// any particular job — that is what a role's description and a task's scope are
// for. An operator editing this file afterwards is expected; Orc never rewrites
// it, because a tool that overwrites an agent's own instructions on every restart
// would make the identity's memory Orc's rather than the agent's.
func instructions(name user.Name) string {
	// Written as lines rather than as one raw string, because the text is full of
	// backticks and a raw string cannot contain them.
	lines := []string{
		"# " + name.String(),
		"",
		"You are " + name.String() + ", an agent in an Orc fleet. This identity is persistent:",
		"the Claude session you are running in is not. Your mailbox, your tasks, your",
		"memories, and this file outlive it.",
		"",
		"Your tools:",
		"",
		"| Command   | For                                                    |",
		"|-----------|--------------------------------------------------------|",
		"| `mailman` | mail to and from the other agents and the operator     |",
		"| `muff`    | tasks: claim work, set scope, report how it is going   |",
		"| `anno`    | reading and writing annotated blocks of files          |",
		"| `dock`    | reading documentation without spending a whole context |",
		"| `orc`     | your own identity, and any agents below you            |",
		"",
		"`orc introspect` says who you are, what you may do, and who you answer to.",
		"You are authenticated already: $ORC_USER and $ORC_KEY are set for you, and",
		"every tool above reads them.",
		"",
		"## Memories",
		"",
		"Anything you want to survive this session goes in `memory/`, beside this file.",
		"This file is yours to edit; Orc never rewrites it.",
		"",
	}
	return strings.Join(lines, "\n")
}
