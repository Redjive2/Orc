// Command claude is a stand-in for the real thing, for Orc's tests.
//
// Nothing in Orc's test suite starts a real Claude session: it would need a
// credential, cost money, and make every test a network test. This is what
// `$ORC_CLAUDE_BIN` points at instead — a program that behaves like an
// interactive terminal session in the ways the supervisor actually depends on.
//
// It is a real program rather than a shell script because the supervisor's job is
// to hold a *terminal*, and the things worth testing — that output reaches an
// attacher, that a poke arrives as keystrokes, that a crash is restarted with the
// same session id — need something that reads its own stdin and can be told to
// misbehave on purpose.
//
// Behaviour, all driven by the environment so a test can choose it per session:
//
//	FAKE_CLAUDE_GREETING   printed on startup, so an attach has something to see
//	FAKE_CLAUDE_EXIT       exit with this status immediately, to test restarts
//	FAKE_CLAUDE_HANG       ignore SIGTERM, to test the escalation to SIGKILL
//	FAKE_CLAUDE_ECHO       echo every line back with this prefix (default "you said: ")
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	// The flags Orc passes are echoed back, which is what lets a test assert the
	// command line without reaching inside the supervisor.
	fmt.Printf("fake-claude args: %s\r\n", strings.Join(os.Args[1:], " "))
	fmt.Printf("fake-claude session: %s\r\n", os.Getenv("ORC_SESSION"))
	fmt.Printf("fake-claude identity: %s\r\n", os.Getenv("ORC_USER"))

	if code := os.Getenv("FAKE_CLAUDE_EXIT"); code != "" {
		n, err := strconv.Atoi(code)
		if err != nil {
			n = 1
		}
		fmt.Printf("fake-claude: exiting %d on purpose\r\n", n)
		os.Exit(n)
	}

	if greeting := os.Getenv("FAKE_CLAUDE_GREETING"); greeting != "" {
		fmt.Printf("%s\r\n", greeting)
	}
	fmt.Print("fake-claude ready\r\n")

	if os.Getenv("FAKE_CLAUDE_HANG") != "" {
		// Deliberately deaf to a polite stop, so the grace period and the SIGKILL
		// after it are exercised rather than assumed.
		signal.Ignore(syscall.SIGTERM, syscall.SIGINT)
	}

	prefix := os.Getenv("FAKE_CLAUDE_ECHO")
	if prefix == "" {
		prefix = "you said: "
	}

	// A line at a time, echoed back with a prefix: enough for a test to prove a poke
	// arrived as keystrokes and came back out as output.
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			continue
		}
		fmt.Printf("%s%s\r\n", prefix, line)
		if line == "quit" {
			fmt.Print("fake-claude: leaving\r\n")
			return
		}
	}

	// Stdin closed. A real session would exit here too; waiting a moment first
	// keeps a test from racing the supervisor's own teardown.
	time.Sleep(50 * time.Millisecond)
}
