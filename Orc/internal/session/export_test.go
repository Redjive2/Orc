package session

// RecordEndingFor runs the ending record for a test.
//
// Exported to the test build only: what a supervisor writes down on its way out is
// the input to every recovery, and testing it through a real child that has to be
// killed at the right moment would be a test about timing rather than about the
// decision.
func RecordEndingFor(s *Supervisor) { s.recordEnding() }
