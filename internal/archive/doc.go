// Package archive defines the format-independent, read-only archive API.
//
// Archive paths form their own POSIX namespace rooted at "/". Drivers expose
// raw entries and file streams; Source validates and indexes those entries,
// including directories implied by nested file paths. The package includes a
// ZIP driver and format-independent safe extraction; other formats can use the
// same contracts without changing callers.
package archive
