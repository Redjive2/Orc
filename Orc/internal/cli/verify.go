package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"orc/common/fault"
)

// verify walks the store and reports what is wrong, without changing anything.
//
// A store several unsupervised agents write to needs a way to answer "is this
// healthy?" that is not "read the source". It is additive: nothing else depends on
// it, and it never repairs, because an automatic repair of damage nobody has
// understood is how one bad file becomes many.
//
// It is also the only command that reports the *soft* problems the derivation
// tolerates — a role removed from under an identity, a permission a role still
// names — because those fail closed everywhere else and would otherwise be
// invisible until somebody wondered why an agent could do nothing.
func (a App) verify(args []string) error {
	if err := exactly(args, 0, "verify takes no arguments"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}

	var problems []string
	report := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if err := a.say(fmt.Sprintf("fleet: %s", a.out.Value(s.store.Root()))); err != nil {
		return err
	}

	// The operator file and the tree must agree. They are two statements of the
	// same fact, and a disagreement means an identity's boss was hand-edited.
	recorded, err := s.store.Operator()
	if err != nil {
		report("the operator is not recorded: %v", err)
	} else if recorded.String() != s.fleet.Operator().String() {
		report("the store records %s as operator, but %s is the identity with no boss",
			recorded, s.fleet.Operator())
	}

	identities, err := s.store.Identities()
	if err != nil {
		return err
	}
	for _, i := range identities {
		name := i.Name()

		// An interrupted append is recovered by the fold, so it is not damage —
		// but a store accumulating them is a store something keeps killing, and
		// that is worth saying.
		if _, skipped, err := s.store.InspectIdentity(name); err != nil {
			report("%s: journal will not replay: %v", name, err)
			continue
		} else if skipped > 0 {
			report("%s: %d bytes at the end of the journal were left by an interrupted write", name, skipped)
		}

		// Half a credential is the one shape a crash during `orc new identity` can
		// leave behind, and it is invisible until somebody tries to act as that
		// identity.
		ok, err := s.store.HasCredential(name)
		if err != nil {
			report("%s: credential could not be checked: %v", name, err)
		} else if !ok {
			report("%s: has no usable credential; `orc remove identity %s --yes` and re-create it", name, name)
		}

		if !i.Role().Zero() {
			if _, exists := s.fleet.Role(i.Role()); !exists {
				report("%s: holds role %s, which does not exist", name, i.Role())
			}
		}
		for _, g := range i.Grants() {
			if _, exists := s.fleet.Permission(g.Permission()); !exists {
				report("%s: was granted %s, which does not exist", name, g.Permission())
			}
		}

		effective, asked := s.fleet.Authority(name)
		line := fmt.Sprintf("  %-20s authority %-4s %2d clause%s",
			name.String(), effective.String(), len(s.fleet.Clauses(name)), plural(len(s.fleet.Clauses(name))))
		if !asked.Zero() && asked.Int() != effective.Int() {
			line += fmt.Sprintf("   capped from %s", asked)
		}
		if err := a.say(line); err != nil {
			return err
		}
	}

	roles, err := s.store.Roles()
	if err != nil {
		return err
	}
	for _, r := range roles {
		if _, skipped, err := s.store.InspectRole(r.Name()); err != nil {
			report("role %s: journal will not replay: %v", r.Name(), err)
		} else if skipped > 0 {
			report("role %s: %d bytes at the end of the journal were left by an interrupted write", r.Name(), skipped)
		}
		for _, p := range r.Permissions() {
			perm, exists := s.fleet.Permission(p)
			if !exists {
				report("role %s: grants %s, which does not exist", r.Name(), p)
				continue
			}
			// Not damage, but a clause that can never apply — which reads as a
			// permission that does not work.
			if !r.Authority().AtLeast(perm.Floor()) {
				report("role %s: holds %s, whose floor %s is above the role's authority %s",
					r.Name(), p, perm.Floor(), r.Authority())
			}
		}
	}

	permissions, err := s.store.Permissions()
	if err != nil {
		return err
	}
	if err := a.say(fmt.Sprintf("  %-20s %d role%s · %d permission%s",
		"policy", len(roles), plural(len(roles)), len(permissions), plural(len(permissions)))); err != nil {
		return err
	}

	// The worklist against what is actually running. Everything above this point
	// asks whether the *store* is coherent; this asks whether the store and the
	// world agree, which is the half an operator notices first — an agent that
	// was employed and is not thinking looks like a broken tool long before
	// anybody suspects a stale pid file.
	for _, i := range identities {
		name := i.Name()
		state, live, err := s.store.Session(name)
		if err != nil {
			report("%s: the session file will not parse: %v; `orc fire %s --yes` and re-employ", name, err, name)
			continue
		}

		if i.Employed() && !live {
			detail := "no session is running"
			if state.LastExit != "" {
				detail = "last exit: " + state.LastExit
			}
			report("%s: on the worklist but not running (%s); `orc tend` will restart it", name, detail)
		}

		if !live {
			// A socket left behind by a dead supervisor is the shape a kill -9
			// leaves. Harmless, but it is the file an operator finds and believes.
			if state.Socket != "" {
				if _, statErr := os.Stat(state.Socket); statErr == nil {
					report("%s: a socket at %s with no session behind it; `orc tend` clears it", name, state.Socket)
				}
			}
			continue
		}

		// A live supervisor that is not orc-session means the pid was recycled:
		// the state file points at somebody else's process, and every liveness
		// check on it will answer yes for the wrong reason.
		if owner, known := supervisorCommand(state.Supervisor); known && !strings.Contains(owner, "orc-session") {
			report("%s: supervisor pid %d is %q, not an orc session — the pid was reused; `orc fire %s --yes` and re-employ",
				name, state.Supervisor, owner, name)
		}
	}

	// The derivation's own tolerated problems, which nothing else surfaces.
	for _, p := range s.fleet.Problems() {
		report("%s", p)
	}

	if len(problems) == 0 {
		return a.say("\n" + a.out.Good("no problems found"))
	}
	if err := a.say(fmt.Sprintf("\n%d problem%s:", len(problems), plural(len(problems)))); err != nil {
		return err
	}
	for _, p := range problems {
		if err := a.say("  " + a.out.Warn(p)); err != nil {
			return err
		}
	}
	// A damaged store is a real failure, so the exit code says so and a script can
	// branch on it.
	return fault.Conflict{Path: s.store.Root(), Reason: fmt.Sprintf("%d problem%s found", len(problems), plural(len(problems)))}
}

// supervisorCommand reports what a pid is actually running.
//
// It exists because liveness answers "is something there", and that is the wrong
// question for a pid read out of a file written days ago: the operating system
// reuses pids, so a state file can point at an unrelated process that answers
// yes. Asking what the process *is* turns "running" into "running the thing that
// claims to be running".
//
// A pid that cannot be inspected returns known=false rather than a guess. Not
// knowing is not evidence of anything, and reporting a healthy session as damage
// would send an operator to fire it.
func supervisorCommand(pid int) (command string, known bool) {
	if pid <= 0 {
		return "", false
	}
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", false
	}
	command = strings.TrimSpace(string(out))
	return command, command != ""
}
