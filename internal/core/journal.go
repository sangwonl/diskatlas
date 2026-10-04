package core

// journalChange carries the path and native event flags needed to update the
// manifest without rescanning unrelated parts of the analysis root.
type journalChange struct {
	Path  string
	Flags uint32
}
