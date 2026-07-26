package source

// ChildEnv exposes the environment handed to Mailman and Macmuffin, so a test
// can assert on the identity the child would actually resolve rather than on
// the intention behind it.
func ChildEnv(c *CLI) []string { return c.childEnv() }
