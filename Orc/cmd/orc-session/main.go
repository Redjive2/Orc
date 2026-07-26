// Command orc-session holds one Claude session open.
//
// It is spawned by `orc employ` and `orc tend`, never run by hand in ordinary use,
// and it is the one binary in this tree that does not exit: the session it holds
// has to outlive the `orc` that started it. Everything about why it is shaped this
// way is in internal/session and Claude/Docs/Orc/Plan.md §6.1.
//
// It takes an identity and, optionally, the session id to run under. With no id it
// mints one, which is what makes `orc-session --identity x` on its own a usable
// recovery step for an operator whose supervisor was killed.
package main

import (
	"fmt"
	"os"
	"strings"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/model"
	"orc/orc/internal/session"
	"orc/orc/internal/store"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	var identity, id, modelName, effortName, root string
	var resume bool

	for i := 0; i < len(args); i++ {
		next := func() string {
			if i+1 >= len(args) {
				return ""
			}
			i++
			return args[i]
		}
		switch args[i] {
		case "--identity":
			identity = next()
		case "--session-id":
			id = next()
		case "--model":
			modelName = next()
		case "--effort":
			effortName = next()
		case "--root":
			root = next()
		case "--resume":
			resume = true
		default:
			return fail(fault.Usage{Reason: fmt.Sprintf("unknown option %q", args[i])})
		}
	}

	name, err := user.Parse(identity)
	if err != nil {
		return fail(fault.Usage{Reason: "orc-session needs --identity <name>: " + err.Error()})
	}

	if root == "" {
		home, _ := os.UserHomeDir()
		if root, err = store.DefaultRoot(store.Env(os.LookupEnv), home); err != nil {
			return fail(err)
		}
	}
	s, err := store.Open(root, clock.Real{})
	if err != nil {
		return fail(err)
	}

	// The load comes from the worklist when it is not given, so a supervisor
	// started by hand runs the identity at what it was employed at rather than at
	// a default somebody has to remember.
	who, err := s.Identity(name)
	if err != nil {
		return fail(err)
	}
	m, e := who.Model(), who.Effort()
	if modelName != "" {
		if m, err = model.ParseModel(modelName); err != nil {
			return fail(err)
		}
	}
	if effortName != "" {
		if e, err = model.ParseEffort(effortName); err != nil {
			return fail(err)
		}
	}
	if !m.Valid() {
		m = model.DefaultModel
	}
	if !e.Valid() {
		e = model.DefaultEffort
	}

	if id == "" {
		if id, err = session.NewID(); err != nil {
			return fail(err)
		}
	}

	env, err := session.Environment(s, name, id)
	if err != nil {
		return fail(err)
	}

	sup, err := session.New(s, session.Spec{
		Identity: name, ID: id, Model: m, Effort: e, Resume: resume,
	}, env, strings.TrimSpace(os.Getenv(session.EnvClaude)))
	if err != nil {
		return fail(err)
	}

	// Signals are the supervisor's own business: a SIGTERM to it means "end the
	// session", not "die and leave a child holding a terminal".
	session.OnSignal(sup.Stop)

	if err := sup.Run(); err != nil {
		return fail(err)
	}
	return fault.CodeOK
}

func fail(err error) int {
	_, _ = fmt.Fprintf(os.Stderr, "orc-session: %v\n", err)
	return fault.Code(err)
}
