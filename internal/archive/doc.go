// Package archive defines the format-independent, read-only archive API.
//
// Archive paths form their own POSIX namespace rooted at "/". Drivers expose
// raw entries and file streams; Source validates and indexes those entries,
// including directories implied by nested file paths. The package includes a
// ZIP, TAR and TAR.GZ drivers and format-independent safe extraction. Plain
// TAR supports offset-based random access. Individual TAR.GZ Open calls
// deliberately rescan the compressed stream, while Extractor uses an optional
// one-pass batch stream for multiple files. Both strategies bound memory and
// avoid a decompressed disk cache.
package archive
