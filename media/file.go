package media

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// File is a file to send with an upload.
type File struct {
	// Name is the filename sent to the server; its extension sets the MIME type.
	Name string
	// Body supplies the file contents.
	Body io.Reader
	// Size is the length in bytes, or -1 if unknown. A known size lets
	// clients validate limits up front and send a Content-Length.
	Size int64
}

// OpenFile opens a file from disk. Close it when finished.
func OpenFile(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if st.IsDir() {
		f.Close()
		return nil, fmt.Errorf("%s is a directory", path)
	}
	return &File{Name: filepath.Base(path), Body: f, Size: st.Size()}, nil
}

// Close closes the file's Body if it is an io.Closer.
func (f *File) Close() error {
	if f == nil {
		return nil
	}
	if c, ok := f.Body.(io.Closer); ok {
		return c.Close()
	}
	return nil
}
