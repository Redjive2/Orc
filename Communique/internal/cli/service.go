package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"orc/cq/internal/fault"
	"orc/cq/internal/style"
)

// Surviving a reboot.
//
// The supervisor keeps the server up while the machine is up: a crash brings it
// back, an upgrade replaces it. What neither it nor anything else in this tree
// does is start the server when the machine starts, or bring it back if the
// supervisor itself is killed — and a site that needs somebody to log in and run a
// command after every power cut is not a site anybody can rely on.
//
// That job belongs to the operating system, which already has something for it and
// has for decades. So this writes the unit file and stops. It does not load it, it
// does not start anything, and it does not ask for a password: installing a service
// is the operator's decision to make on their own machine, and a tool that did it
// as a side effect of a flag would be a tool that installed a background service
// somebody did not ask for.
//
// The two units say the same thing in two dialects:
//
//   - start it at login, and again if it fails;
//   - do **not** start it again if it exited cleanly, because a clean exit is
//     somebody having asked it to stop — the same rule the supervisor follows one
//     level down, for the same reason;
//   - wait ten seconds between attempts, which is the outer bound on the
//     supervisor's own backoff and stops two restart loops from compounding.

// serviceLabel names the job to the operating system. Reverse-DNS because launchd
// expects it and systemd does not mind, so one name works for both.
const serviceLabel = "orc.cq"

// servicePlan is everything a unit file needs to say.
//
// It is a value rather than a set of lookups so the whole thing can be rendered
// for a platform this is not running on, which is the only way the Windows and
// Linux text can be pinned by tests that run on a Mac.
type servicePlan struct {
	// GOOS is the platform the unit is for.
	GOOS string
	// Home is the user's home directory, and every path below is under it. A
	// parameter rather than a lookup so a test can point it somewhere disposable.
	Home string
	// Exe and Args are the command the service runs. Resolved values, never the
	// raw command line: launchd starts a job with almost none of a login shell's
	// environment, so anything that came from $CQ_STATE at the prompt has to be
	// written down here or the service will quietly use a different store.
	Exe  string
	Args []string
	// Env is what the service needs that is not an argument. PATH above all: an
	// upgrade shells out to git and to the build script, and a launchd job's PATH
	// is `/usr/bin:/bin:/usr/sbin:/sbin` unless it is told otherwise — which is a
	// PATH with no git in it on most machines.
	Env map[string]string
}

// service writes the unit for this machine and says how to load it.
func (a App) service(plan servicePlan, force bool) error {
	path, body, load, err := unitFor(plan)
	if err != nil {
		return err
	}

	// An operator who has edited their own unit should not lose it to a flag. The
	// identical case is silent about it, because re-running this after changing a
	// flag is the ordinary way to update one.
	switch existing, err := os.ReadFile(path); {
	case err == nil && string(existing) == body:
		if err := a.say("%s is already installed and unchanged", a.ink(path, style.Value)); err != nil {
			return err
		}
		return a.sayLoad(path, load)
	case err == nil && !force:
		return fault.Conflict{Subject: path, Reason: "a different service is already installed here; " +
			"read it, then pass --force to replace it"}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fault.IO{Op: "create", Subject: filepath.Dir(path), Err: err}
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return fault.IO{Op: "write", Subject: path, Err: err}
	}
	if err := a.say("wrote %s", a.ink(path, style.Value)); err != nil {
		return err
	}
	return a.sayLoad(path, load)
}

// sayLoad prints the commands that turn a file into a running service.
//
// Printed rather than run. Loading a service starts a long-lived background
// process under the operator's account; that is theirs to do knowingly, and the
// difference between a tool that offers and a tool that helps itself is the whole
// reason this stops here.
func (a App) sayLoad(path string, load []string) error {
	if err := a.say("\n%s", a.ink("to start it, and start it at every login:", style.Quiet)); err != nil {
		return err
	}
	for _, line := range load {
		if err := a.say("  %s", line); err != nil {
			return err
		}
	}
	return nil
}

// unitFor renders the unit file for a platform: where it goes, what is in it, and
// what loads it.
func unitFor(p servicePlan) (path, body string, load []string, err error) {
	if strings.TrimSpace(p.Home) == "" {
		return "", "", nil, fault.Internal{Where: "cli.unitFor", Detail: "no home directory"}
	}
	switch p.GOOS {
	case "darwin":
		path = filepath.Join(p.Home, "Library", "LaunchAgents", serviceLabel+".plist")
		return path, launchdPlist(p), []string{
			fmt.Sprintf("launchctl bootstrap gui/$(id -u) %s", path),
			fmt.Sprintf("launchctl kickstart -p gui/$(id -u)/%s", serviceLabel),
		}, nil

	case "linux":
		path = filepath.Join(p.Home, ".config", "systemd", "user", serviceLabel+".service")
		return path, systemdUnit(p), []string{
			"systemctl --user daemon-reload",
			"systemctl --user enable --now " + serviceLabel,
			// Without lingering, a user service stops at logout and does not come
			// back until the next login — which is not "survives a reboot" on a
			// headless box, and is the trap this whole feature exists to avoid.
			"sudo loginctl enable-linger $USER   # so it runs without you logged in",
		}, nil

	default:
		// Named rather than shrugged at. Windows has no equivalent worth
		// generating: the Service Control Manager will not run an ordinary console
		// program as a service without a wrapper, and writing a Task Scheduler XML
		// that half-works would be worse than saying plainly what does.
		return "", "", nil, fault.Usage{Reason: fmt.Sprintf(
			"cq cannot write a service file for %s; on windows use Task Scheduler "+
				"(\"at log on\", restart on failure) or a wrapper such as NSSM, "+
				"pointing either at: %s", p.GOOS, strings.Join(append([]string{p.Exe}, p.Args...), " "))}
	}
}

// launchdPlist is the macOS unit.
func launchdPlist(p servicePlan) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" ` +
		`"http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")

	b.WriteString("  <key>Label</key>\n  <string>" + xmlText(serviceLabel) + "</string>\n")

	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, arg := range append([]string{p.Exe}, p.Args...) {
		b.WriteString("    <string>" + xmlText(arg) + "</string>\n")
	}
	b.WriteString("  </array>\n")

	b.WriteString("  <key>RunAtLoad</key>\n  <true/>\n")

	// KeepAlive on failure only. `SuccessfulExit: false` reads as "keep it alive
	// unless it exited successfully", which is exactly the supervisor's own rule:
	// a clean exit was asked for and must stay.
	b.WriteString("  <key>KeepAlive</key>\n  <dict>\n")
	b.WriteString("    <key>SuccessfulExit</key>\n    <false/>\n")
	b.WriteString("  </dict>\n")

	// The outer bound on the supervisor's backoff, so the two do not compound into
	// a machine that is busy doing nothing.
	b.WriteString("  <key>ThrottleInterval</key>\n  <integer>10</integer>\n")

	if len(p.Env) > 0 {
		b.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
		for _, k := range sortedKeys(p.Env) {
			b.WriteString("    <key>" + xmlText(k) + "</key>\n")
			b.WriteString("    <string>" + xmlText(p.Env[k]) + "</string>\n")
		}
		b.WriteString("  </dict>\n")
	}

	// launchd discards a job's output unless it is told where to put it, and a
	// server whose only account of itself went to /dev/null is a server nobody can
	// debug after the fact.
	logs := filepath.Join(p.Home, "Library", "Logs", "cq.log")
	b.WriteString("  <key>StandardOutPath</key>\n  <string>" + xmlText(logs) + "</string>\n")
	b.WriteString("  <key>StandardErrorPath</key>\n  <string>" + xmlText(logs) + "</string>\n")

	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// systemdUnit is the Linux one.
func systemdUnit(p servicePlan) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=communiqué — the Orc fleet's website\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n\n")

	b.WriteString("[Service]\n")
	b.WriteString("ExecStart=" + shellJoin(append([]string{p.Exe}, p.Args...)) + "\n")
	// on-failure, not always: a clean exit is somebody having asked it to stop.
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=10\n")
	for _, k := range sortedKeys(p.Env) {
		b.WriteString("Environment=" + k + "=" + p.Env[k] + "\n")
	}
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return b.String()
}

// xmlText escapes a value for a plist. Paths carry ampersands more often than
// anybody expects, and an unescaped one makes the file unparseable — which
// launchd reports as a job that does not exist rather than a file that is wrong.
func xmlText(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

// shellJoin quotes what systemd would otherwise split. A state directory under
// "Application Support" is two arguments without this.
func shellJoin(args []string) string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if strings.ContainsAny(a, " \t\"'") {
			out = append(out, `"`+strings.ReplaceAll(a, `"`, `\"`)+`"`)
			continue
		}
		out = append(out, a)
	}
	return strings.Join(out, " ")
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// installService builds the plan from the server this command line describes.
//
// Every value is the *resolved* one, not the flag as typed. A service starts with
// none of the environment a login shell has, so a store that came from $CQ_STATE
// at the prompt has to be written into the unit as an argument — otherwise the
// service runs against the default store and serves an empty site, which is the
// kind of wrong that looks like data loss.
func (a App) installService(addr, state, cert, key string, noAdmin bool, source, bin string, force bool) error {
	exe, err := restartable()
	if err != nil {
		// The same refusal `serve` gives, and for the same reason: a binary the
		// system will not start is not one to write into a startup service.
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fault.IO{Op: "find", Subject: "your home directory", Err: err}
	}

	args := []string{"serve", "--addr", addr, "--state", state}
	if cert != "" {
		args = append(args, "--tls-cert", cert)
	}
	if key != "" {
		args = append(args, "--tls-key", key)
	}
	if noAdmin {
		args = append(args, "--no-admin")
	}
	if source != "" {
		args = append(args, "--source", source)
	}
	if bin != "" {
		args = append(args, "--bin", bin)
	}

	env := map[string]string{}
	// PATH above all. An upgrade shells out to git and to the build script, and a
	// service's PATH has no git on it — so a fleet installed this way would upgrade
	// perfectly until the first time it tried, and then fail in a way that looks
	// nothing like a missing PATH.
	if v, ok := a.Env("PATH"); ok && v != "" {
		env["PATH"] = v
	}
	for _, name := range []string{"ORC_THEME", "ORC_HOME", "MAILMAN_HOME"} {
		if v, ok := a.Env(name); ok && v != "" {
			env[name] = v
		}
	}

	return a.service(servicePlan{
		GOOS: runtime.GOOS, Home: home, Exe: exe, Args: args, Env: env,
	}, force)
}
