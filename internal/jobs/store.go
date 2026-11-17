package jobs

import (
	"os"
)

type jobStore interface {
	lock(id string, fn func() error) error
	existsRecord(id string) (bool, error)
	createLog(id string, appendMode bool) (*os.File, error)
	openLog(id string, appendMode bool) (*os.File, error)
	readRecord(id string) ([]byte, error)
	writeRecord(id string, data []byte, crash func(string)) error
	entries() ([]os.DirEntry, error)
	remove(name string) error
	removeTemps(id string) error
}
