package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// A startup service is written once and read by nobody until the machine reboots
// — which is the worst possible time to discover it was wrong. So the text is
// pinned here, for both platforms, from whichever one is running the tests.

func plan(goos, home string) servicePlan {
	return servicePlan{
		GOOS: goos, Home: home,
		Exe:  "/usr/local/bin/cq",
		Args: []string{"serve", "--addr", ":8080", "--state", "/srv/cq"},
		Env:  map[string]string{"PATH": "/usr/local/bin:/usr/bin:/bin"},
	}
}

// --- where it goes ---------------------------------------------------------

func TestTheUnitGoesWhereThePlatformLooksForIt(t *testing.T) {
	home := "/home/redjive"

	mac, _, _, err := unitFor(plan("darwin", home))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "Library", "LaunchAgents", "orc.cq.plist"); mac != want {
		t.Errorf("launchd unit at %s, want %s", mac, want)
	}

	linux, _, _, err := unitFor(plan("linux", home))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".config", "systemd", "user", "orc.cq.service"); linux != want {
		t.Errorf("systemd unit at %s, want %s", linux, want)
	}
}

// --- what it says ----------------------------------------------------------

// The two rules that make this a service rather than a way to start something
// once: it comes up at login, and it comes back if it fails.
func TestTheServiceStartsAtLoginAndComesBackFromFailure(t *testing.T) {
	_, mac, _, _ := unitFor(plan("darwin", "/home/x"))
	for _, want := range []string{"<key>RunAtLoad</key>", "<true/>", "<key>KeepAlive</key>"} {
		if !strings.Contains(mac, want) {
			t.Errorf("the launchd unit is missing %q:\n%s", want, mac)
		}
	}

	_, unit, _, _ := unitFor(plan("linux", "/home/x"))
	for _, want := range []string{"Restart=on-failure", "WantedBy=default.target"} {
		if !strings.Contains(unit, want) {
			t.Errorf("the systemd unit is missing %q:\n%s", want, unit)
		}
	}
}

// And the rule that keeps it usable: a clean exit is somebody having asked it to
// stop, and a service that restarted through that would be one nobody could turn
// off without deleting the unit. This is the same decision the supervisor makes a
// level down — see decide.
func TestACleanlyStoppedServiceStaysStopped(t *testing.T) {
	_, mac, _, _ := unitFor(plan("darwin", "/home/x"))
	// KeepAlive{SuccessfulExit: false} — "keep it alive unless it exited well".
	if !strings.Contains(mac, "<key>SuccessfulExit</key>\n    <false/>") {
		t.Errorf("launchd would restart a server that was asked to stop:\n%s", mac)
	}
	if strings.Contains(mac, "<key>KeepAlive</key>\n  <true/>") {
		t.Error("KeepAlive is unconditional, so the server could not be stopped")
	}

	_, unit, _, _ := unitFor(plan("linux", "/home/x"))
	if strings.Contains(unit, "Restart=always") {
		t.Errorf("systemd would restart a server that was asked to stop:\n%s", unit)
	}
}

// The resolved command, not the one somebody typed. A service starts with almost
// none of a login shell's environment, so a store that came from $CQ_STATE has to
// be an argument in the file or the service serves a different, empty site.
func TestTheUnitRecordsTheWholeCommand(t *testing.T) {
	_, mac, _, _ := unitFor(plan("darwin", "/home/x"))
	for _, want := range []string{"/usr/local/bin/cq", "serve", "--addr", ":8080", "--state", "/srv/cq"} {
		if !strings.Contains(mac, "<string>"+want+"</string>") {
			t.Errorf("the launchd unit does not name %q:\n%s", want, mac)
		}
	}

	_, unit, _, _ := unitFor(plan("linux", "/home/x"))
	if !strings.Contains(unit, "ExecStart=/usr/local/bin/cq serve --addr :8080 --state /srv/cq") {
		t.Errorf("the systemd ExecStart is not the whole command:\n%s", unit)
	}
}

// PATH in particular. An upgrade shells out to git and to the build script, and a
// service's PATH has no git on it — so a fleet installed without this would
// upgrade fine until the first time it tried.
func TestTheUnitCarriesThePathAnUpgradeNeeds(t *testing.T) {
	_, mac, _, _ := unitFor(plan("darwin", "/home/x"))
	if !strings.Contains(mac, "<key>PATH</key>") {
		t.Errorf("the launchd unit has no PATH, so an upgrade would not find git:\n%s", mac)
	}
	_, unit, _, _ := unitFor(plan("linux", "/home/x"))
	if !strings.Contains(unit, "Environment=PATH=/usr/local/bin:/usr/bin:/bin") {
		t.Errorf("the systemd unit has no PATH:\n%s", unit)
	}
}

// --- the awkward inputs ----------------------------------------------------

// A path with an ampersand makes an unescaped plist unparseable, which launchd
// reports as a job that does not exist rather than a file that is wrong — an hour
// of looking in the wrong place.
func TestAPathWithMarkupInItIsEscaped(t *testing.T) {
	p := plan("darwin", "/home/x")
	p.Args = []string{"serve", "--state", "/srv/R&D <live>"}

	_, mac, _, _ := unitFor(p)
	if strings.Contains(mac, "R&D") {
		t.Errorf("an ampersand went into the plist raw, so launchd cannot parse it:\n%s", mac)
	}
	if !strings.Contains(mac, "R&amp;D &lt;live&gt;") {
		t.Errorf("the escaped path is not in the plist:\n%s", mac)
	}
}

// systemd splits ExecStart on spaces, so "Application Support" is two arguments
// unless it is quoted — and the service then starts against the wrong store.
func TestAPathWithSpacesSurvivesSystemd(t *testing.T) {
	p := plan("linux", "/home/x")
	p.Args = []string{"serve", "--state", "/home/x/Application Support/cq"}

	_, unit, _, _ := unitFor(p)
	if !strings.Contains(unit, `"/home/x/Application Support/cq"`) {
		t.Errorf("a state directory with a space is unquoted, so systemd will split it:\n%s", unit)
	}
}

// --- what cannot be done ---------------------------------------------------

// Windows gets a refusal that names what to use instead. Generating a
// half-working Task Scheduler job would be worse than saying plainly what does
// work — the whole point of this feature is a server that is reliably up.
func TestWindowsIsToldWhatToUseInstead(t *testing.T) {
	_, _, _, err := unitFor(plan("windows", `C:\Users\redjive`))
	if err == nil {
		t.Fatal("a service file was generated for windows, where it cannot work")
	}
	got := err.Error()
	for _, want := range []string{"Task Scheduler", "NSSM", "cq"} {
		if !strings.Contains(got, want) {
			t.Errorf("the refusal does not mention %q: %s", want, got)
		}
	}
	// And it hands over the command, so the operator can paste it in rather than
	// reconstruct it.
	if !strings.Contains(got, "serve --addr :8080 --state /srv/cq") {
		t.Errorf("the refusal does not give the command to schedule: %s", got)
	}
}

func TestWithoutAHomeThereIsNowhereToPutIt(t *testing.T) {
	if _, _, _, err := unitFor(plan("darwin", "")); err == nil {
		t.Error("a unit was generated with no home directory to put it in")
	}
}

// --- loading it ------------------------------------------------------------

// The commands are printed, never run. What they must not do is leave out the
// step that makes "survives a reboot" true: a systemd *user* service stops at
// logout and does not come back until the next login unless lingering is on,
// which on a headless box means it never comes back at all.
func TestTheLinuxInstructionsMentionLingering(t *testing.T) {
	_, _, load, _ := unitFor(plan("linux", "/home/x"))
	joined := strings.Join(load, "\n")
	if !strings.Contains(joined, "enable-linger") {
		t.Errorf("the instructions omit lingering, so the service would not survive a logout:\n%s", joined)
	}
	if !strings.Contains(joined, "daemon-reload") {
		t.Errorf("the instructions omit daemon-reload, so systemd would not see the new unit:\n%s", joined)
	}
}

func TestTheMacInstructionsLoadTheFileThatWasWritten(t *testing.T) {
	path, _, load, _ := unitFor(plan("darwin", "/home/x"))
	joined := strings.Join(load, "\n")
	if !strings.Contains(joined, path) {
		t.Errorf("the instructions do not name the file that was written:\n%s", joined)
	}
	if !strings.Contains(joined, "launchctl") {
		t.Errorf("the instructions do not say how to load it:\n%s", joined)
	}
}
