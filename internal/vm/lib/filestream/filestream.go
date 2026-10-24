package filestream

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/marcgauthier/logmetrics/internal/cryptofs"
	"github.com/marcgauthier/logmetrics/internal/vm-metrics"
	"github.com/marcgauthier/logmetrics/internal/vm/lib/fs/fsutil"
	"github.com/marcgauthier/logmetrics/internal/vm/lib/logger"
	"github.com/marcgauthier/logmetrics/internal/vm/lib/memory"
)

// disableFadvise disables the fadvise() syscall when reading large data files.
// The fadvise() syscall prevents from eviction of recently accessed data from
// OS page cache during background merges and backups. In some rare cases it is
// better to disable the syscall if it uses too much CPU.
var disableFadvise = false

const dontNeedBlockSize = 16 * 1024 * 1024

// ReadCloser is a standard interface for filestream Reader.
type ReadCloser interface {
	Path() string
	Read(p []byte) (int, error)
	MustClose()
}

// WriteCloser is a standard interface for filestream Writer.
type WriteCloser interface {
	Path() string
	Write(p []byte) (int, error)
	MustClose()
}

func getReadBufferSize() int {
	readBufferSizeOnce.Do(func() {
		n := min(max(memory.Allowed()/1024/64, 4*1024), 64*1024)
		readBufferSize = n
	})
	return readBufferSize
}

var (
	readBufferSize     int
	readBufferSizeOnce sync.Once
)

func getWriteBufferSize() int {
	writeBufferSizeOnce.Do(func() {
		n := min(max(memory.Allowed()/1024/8, 4*1024), 128*1024)
		writeBufferSize = n
	})
	return writeBufferSize
}

var (
	writeBufferSize     int
	writeBufferSizeOnce sync.Once
)

// Reader implements buffered file reader.
type Reader struct {
	f      *os.File
	br     *bufio.Reader
	crypto *cryptofs.Reader
	st     streamTracker
}

// Path returns the path to r
func (r *Reader) Path() string {
	return r.f.Name()
}

// OpenReaderAt opens the file at the given path in nocache mode at the given offset.
//
// If nocache is set, then the reader doesn't pollute OS page cache.
func OpenReaderAt(path string, offset int64, nocache bool) (*Reader, error) {
	r := MustOpen(path, nocache)
	var n int64
	var err error
	if r.crypto != nil {
		n, err = r.crypto.Seek(offset, io.SeekStart)
		r.br.Reset(r.crypto)
	} else {
		n, err = r.f.Seek(offset, io.SeekStart)
	}
	if err != nil {
		r.MustClose()
		return nil, fmt.Errorf("cannot seek to offset=%d for %q: %w", offset, path, err)
	}
	if n != offset {
		r.MustClose()
		return nil, fmt.Errorf("invalid seek offset for %q; got %d; want %d", path, n, offset)
	}
	return r, nil
}

// MustOpen opens the file from the given path in nocache mode.
//
// If nocache is set, then the reader doesn't pollute OS page cache.
func MustOpen(path string, nocache bool) *Reader {
	f, err := os.Open(path)
	if err != nil {
		logger.Panicf("FATAL: cannot open file: %s", err)
	}
	r := &Reader{f: f}
	if cryptofs.Enabled(path) {
		cr, err := cryptofs.NewReader(f)
		if err != nil {
			_ = f.Close()
			logger.Panicf("FATAL: cannot authenticate encrypted file %q: %s", path, err)
		}
		r.crypto = cr
		r.br = bufio.NewReaderSize(cr, getReadBufferSize())
	} else {
		r.br = getBufioReader(f)
	}
	if disableFadvise {
		// Unconditionally disable fadvise() syscall
		// See https://github.com/VictoriaMetrics/VictoriaMetrics/pull/5120 for details on why this is needed
		nocache = false
	}
	if nocache {
		r.st.fd = f.Fd()
	}
	readersCount.Inc()
	return r
}

// MustClose closes the underlying file passed to MustOpen.
func (r *Reader) MustClose() {
	if err := r.st.close(); err != nil {
		logger.Panicf("FATAL: cannot close streamTracker for file %q: %s", r.f.Name(), err)
	}
	if err := r.f.Close(); err != nil {
		logger.Panicf("FATAL: cannot close file %q: %s", r.f.Name(), err)
	}
	r.f = nil

	if r.crypto == nil {
		putBufioReader(r.br)
	}
	r.crypto = nil
	r.br = nil

	readersCount.Dec()
}

var (
	readDuration      = metrics.NewFloatCounter(`vm_filestream_read_duration_seconds_total`)
	readCallsBuffered = metrics.NewCounter(`vm_filestream_buffered_read_calls_total`)
	readCallsReal     = metrics.NewCounter(`vm_filestream_real_read_calls_total`)
	readBytesBuffered = metrics.NewCounter(`vm_filestream_buffered_read_bytes_total`)
	readBytesReal     = metrics.NewCounter(`vm_filestream_real_read_bytes_total`)
	readersCount      = metrics.NewCounter(`vm_filestream_readers`)
)

// Read reads file contents to p.
func (r *Reader) Read(p []byte) (int, error) {
	readCallsBuffered.Inc()
	n, err := r.br.Read(p)
	readBytesBuffered.Add(n)
	if err != nil {
		return n, err
	}
	if err := r.st.adviseDontNeed(n, false); err != nil {
		return n, fmt.Errorf("advise error for %q: %w", r.f.Name(), err)
	}
	return n, nil
}

type statReader struct {
	*os.File
}

func (sr *statReader) Read(p []byte) (int, error) {
	startTime := time.Now()
	readCallsReal.Inc()
	n, err := sr.File.Read(p)
	d := time.Since(startTime).Seconds()
	readDuration.Add(d)
	readBytesReal.Add(n)
	return n, err
}

func getBufioReader(f *os.File) *bufio.Reader {
	sr := &statReader{f}
	v := brPool.Get()
	if v == nil {
		return bufio.NewReaderSize(sr, getReadBufferSize())
	}
	br := v.(*bufio.Reader)
	br.Reset(sr)
	return br
}

func putBufioReader(br *bufio.Reader) {
	brPool.Put(br)
}

var brPool sync.Pool

// Writer implements buffered file writer.
type Writer struct {
	f      *os.File
	bw     *bufio.Writer
	crypto *cryptofs.Writer
	st     streamTracker
}

// Path returns the path to r
func (w *Writer) Path() string {
	return w.f.Name()
}

// OpenWriterAt opens the file at path in nocache mode for writing at the given offset.
//
// The file at path is created if it is missing.
//
// If nocache is set, the writer doesn't pollute OS page cache.
func OpenWriterAt(path string, offset int64, nocache bool) (*Writer, error) {
	if cryptofs.Enabled(path) && offset != 0 {
		return nil, fmt.Errorf("encrypted files do not support writing at nonzero offsets")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	n, err := f.Seek(offset, io.SeekStart)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("cannot seek to offset=%d in %q: %w", offset, path, err)
	}
	if n != offset {
		_ = f.Close()
		return nil, fmt.Errorf("invalid seek offset for %q; got %d; want %d", path, n, offset)
	}
	return newWriter(f, nocache), nil
}

// MustCreate creates the file for the given path in nocache mode.
//
// If nocache is set, the writer doesn't pollute OS page cache.
func MustCreate(path string, nocache bool) *Writer {
	f, err := os.Create(path)
	if err != nil {
		logger.Panicf("FATAL: cannot create file %q: %s", path, err)
	}
	return newWriter(f, nocache)
}

func newWriter(f *os.File, nocache bool) *Writer {
	w := &Writer{f: f}
	if cryptofs.Enabled(f.Name()) {
		cw, err := cryptofs.NewWriter(f)
		if err != nil {
			_ = f.Close()
			logger.Panicf("FATAL: cannot create encrypted file %q: %s", f.Name(), err)
		}
		w.crypto = cw
		w.bw = bufio.NewWriterSize(cw, getWriteBufferSize())
	} else {
		w.bw = getBufioWriter(f)
	}
	if nocache {
		w.st.fd = f.Fd()
	}
	writersCount.Inc()
	return w
}

// MustClose syncs the underlying file to storage and then closes it.
func (w *Writer) MustClose() {
	w.flush()

	if w.crypto != nil {
		if err := w.crypto.Finish(); err != nil {
			logger.Panicf("FATAL: cannot finalize encrypted file %q: %s", w.f.Name(), err)
		}
	} else {
		putBufioWriter(w.bw)
	}
	w.crypto = nil
	w.bw = nil

	w.sync()
	if err := w.st.close(); err != nil {
		logger.Panicf("FATAL: cannot close streamTracker for file %q: %s", w.f.Name(), err)
	}
	if err := w.f.Close(); err != nil {
		logger.Panicf("FATAL: cannot close file %q: %s", w.f.Name(), err)
	}
	w.f = nil

	writersCount.Dec()
}

func (w *Writer) flush() {
	if err := w.bw.Flush(); err != nil {
		logger.Panicf("FATAL: cannot flush buffered data to file %q: %s", w.f.Name(), err)
	}
}

func (w *Writer) sync() {
	if !fsutil.IsFsyncDisabled() {
		startTime := time.Now()
		if err := w.f.Sync(); err != nil {
			logger.Panicf("FATAL: cannot sync file %q: %s", w.f.Name(), err)
		}
		d := time.Since(startTime).Seconds()
		fsyncDuration.Add(d)
		fsyncCalls.Inc()
	}
}

var (
	writeDuration        = metrics.NewFloatCounter(`vm_filestream_write_duration_seconds_total`)
	writeCallsBuffered   = metrics.NewCounter(`vm_filestream_buffered_write_calls_total`)
	writeCallsReal       = metrics.NewCounter(`vm_filestream_real_write_calls_total`)
	writtenBytesBuffered = metrics.NewCounter(`vm_filestream_buffered_written_bytes_total`)
	writtenBytesReal     = metrics.NewCounter(`vm_filestream_real_written_bytes_total`)
	writersCount         = metrics.NewCounter(`vm_filestream_writers`)

	fsyncDuration = metrics.NewFloatCounter(`vm_filestream_fsync_duration_seconds_total`)
	fsyncCalls    = metrics.NewCounter(`vm_filestream_fsync_calls_total`)
)

// Write writes p to the underlying file.
func (w *Writer) Write(p []byte) (int, error) {
	writeCallsBuffered.Inc()
	n, err := w.bw.Write(p)
	writtenBytesBuffered.Add(n)
	if err != nil {
		return n, err
	}
	if err := w.st.adviseDontNeed(n, true); err != nil {
		return n, fmt.Errorf("advise error for %q: %w", w.f.Name(), err)
	}
	return n, nil
}

// MustFlush flushes all the buffered data to file.
//
// if isSync is true, then the flushed data is fsynced to the underlying storage.
func (w *Writer) MustFlush(isSync bool) {
	w.flush()
	if isSync {
		w.sync()
	}
}

type statWriter struct {
	*os.File
}

func (sw *statWriter) Write(p []byte) (int, error) {
	startTime := time.Now()
	writeCallsReal.Inc()
	n, err := sw.File.Write(p)
	d := time.Since(startTime).Seconds()
	writeDuration.Add(d)
	writtenBytesReal.Add(n)
	return n, err
}

func getBufioWriter(f *os.File) *bufio.Writer {
	sw := &statWriter{f}
	v := bwPool.Get()
	if v == nil {
		return bufio.NewWriterSize(sw, getWriteBufferSize())
	}
	bw := v.(*bufio.Writer)
	bw.Reset(sw)
	return bw
}

func putBufioWriter(bw *bufio.Writer) {
	bwPool.Put(bw)
}

var bwPool sync.Pool

type streamTracker struct {
	fd     uintptr
	offset uint64 // nolint:unused
	length uint64 // nolint:unused
}
