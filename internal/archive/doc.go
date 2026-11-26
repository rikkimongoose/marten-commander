// Package archive defines the format-independent, read-only archive API.
//
// Archive paths form their own POSIX namespace rooted at "/". Drivers expose
// raw entries and file streams; Source validates and indexes those entries,
// including directories implied by nested file paths. Concrete ZIP, TAR and
// other format drivers are intentionally outside this package.
package archive
