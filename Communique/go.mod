module orc/cq

go 1.26.1

// Orc's tools share one colour scheme. The module is local and never
// published, so it is wired by path rather than by version — which also means
// each tool still builds on its own, without a workspace file.
replace orc/theme => ../Theme

replace orc/common => ../Common

require (
	orc/common v0.0.0
	orc/theme v0.0.0-00010101000000-000000000000
)
