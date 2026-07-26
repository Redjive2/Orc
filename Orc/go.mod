module orc/orc

go 1.26.1

require orc/common v0.0.0

// The packages every Orc tool had grown its own copy of. Local and never
// published, so wired by path — orc still builds on its own, without the
// workspace file.
replace orc/common => ../Common

require orc/theme v0.0.0

// Orc's tools share one colour scheme. The module is local and never published,
// so it is wired by path rather than by version.
replace orc/theme => ../Theme
