package model

import (
	"fmt"

	"orc/common/fault"
)

// The authority range, from Docs/Orc/Auth_Perm_Role.md: "The user has authority
// level 100. All other authority levels must be in range 1-99."
//
// Operator is a constant rather than a configurable ceiling because the whole
// derivation in internal/authz terminates by reaching it. A second identity with
// authority 100 would be a second root, and a tree with two roots is one whose
// caps mean nothing.
const (
	MinAuthority = 1
	MaxAuthority = 99
	Operator     = 100
)

// Authority is a validated authority level. The zero value is not usable;
// construct one with ParseAuthority or NewAuthority.
//
// It is a distinct type rather than an int because it is compared against three
// different things — a role's level, a permission's floor, and a boss's derived
// level — and an int would let any of those be silently swapped for a count or a
// load.
type Authority struct {
	n int
}

// NewAuthority validates a level.
//
// The operator's 100 is accepted here, but only bootstrap has any reason to
// build one: `orc new role` refuses it separately, with a message about there
// being one operator, because "100 is out of range" would be a lie.
func NewAuthority(n int) (Authority, error) {
	if n == Operator {
		return Authority{n: Operator}, nil
	}
	if n < MinAuthority || n > MaxAuthority {
		return Authority{}, fault.Usage{Reason: fmt.Sprintf(
			"authority %d is outside %d-%d; %d is the operator's and cannot be assigned",
			n, MinAuthority, MaxAuthority, Operator)}
	}
	return Authority{n: n}, nil
}

// ParseAuthority reads a level from the command line.
func ParseAuthority(raw string) (Authority, error) {
	n, err := parseInt(raw)
	if err != nil {
		return Authority{}, fault.Usage{Reason: fmt.Sprintf("authority %q is not a whole number", raw)}
	}
	return NewAuthority(n)
}

// OperatorAuthority is the level bootstrap gives the one identity that has it.
func OperatorAuthority() Authority { return Authority{n: Operator} }

// Int returns the level.
func (a Authority) Int() int { return a.n }

// Zero reports whether the level was never constructed.
func (a Authority) Zero() bool { return a.n == 0 }

// String renders the level.
func (a Authority) String() string {
	if a.Zero() {
		return "—"
	}
	return itoa(a.n)
}

// IsOperator reports whether this is the operator's level.
func (a Authority) IsOperator() bool { return a.n == Operator }

// AtLeast reports whether a clears floor. A zero authority clears nothing,
// which is what makes an unconstructed value safe to compare: an identity whose
// authority could not be derived holds no permission at all.
func (a Authority) AtLeast(floor Authority) bool {
	if a.Zero() || floor.Zero() {
		return false
	}
	return a.n >= floor.n
}

// Min returns the lower of two levels, which is the whole of how the tree caps
// authority. A zero on either side wins, because "not derived" must never
// widen anything.
func Min(a, b Authority) Authority {
	switch {
	case a.Zero():
		return a
	case b.Zero():
		return b
	case a.n <= b.n:
		return a
	default:
		return b
	}
}

// Small local helpers, so this package does not import strconv for two
// conversions and so their failure modes are the ones it wants.

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func parseInt(s string) (int, error) {
	if s == "" {
		return 0, fault.Parse{Reason: "empty number"}
	}
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, fault.Parse{Reason: "not a number"}
		}
		n = n*10 + int(c-'0')
		if n > 1<<20 {
			return 0, fault.Parse{Reason: "number is too large"}
		}
	}
	return n, nil
}
