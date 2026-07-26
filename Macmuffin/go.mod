module orc/macmuffin

go 1.26.1

require (
	orc/common v0.0.0
	orc/theme v0.0.0
)

// Local, never published, so wired by path — each tool still builds on its own
// without the workspace.
replace orc/common => ../Common

replace orc/theme => ../Theme
