module orc/anno

go 1.26.1

require orc/theme v0.0.0

// Orc's tools share one colour scheme. The module is local and never
// published, so it is wired by path rather than by version — which also means
// each tool still builds on its own, without a workspace file.
replace orc/theme => ../Theme

require orc/common v0.0.0

// The packages every Orc tool had grown its own copy of. Local and never
// published, so wired by path — each tool still builds on its own.
replace orc/common => ../Common
