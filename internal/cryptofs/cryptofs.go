// Package cryptofs implements authenticated encryption of immutable compressed
// storage files. It is internal to this module and cannot be used outside this module.
package cryptofs

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	blockSize   = 64 * 1024
	headerSize  = 32
	trailerSize = 24
	magic       = "MTXAEAD1"
	keyFilename = ".metrics-key"
)

// The magic must fill the 8-byte header field exactly; both declarations
// fail to compile if it is any other length.
var _ [8 - len(magic)]byte
var _ [len(magic) - 8]byte

var (
	ErrLocked     = errors.New("metrics: encrypted database requires a 32-byte AES key")
	ErrInvalidKey = errors.New("metrics: invalid encryption key or damaged key marker")
	ErrPlaintext  = errors.New("metrics: existing unencrypted database cannot be opened in encrypted mode without migration")
)

type Cipher struct{ aead cipher.AEAD }

var active = struct {
	sync.RWMutex
	roots map[string]*Cipher
}{roots: make(map[string]*Cipher)}

func aesGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("metrics: AES-256-GCM requires exactly 32 key bytes, got %d", len(key))
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// Unlock registers a key only after verifying the encrypted database marker.
// A new encrypted database is created only in an empty directory. It must be
// released when the storage instance closes. Keys never touch the disk.
func Unlock(root string, key []byte) (func(), error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	aead, err := aesGCM(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	active.Lock()
	defer active.Unlock()
	if _, exists := active.roots[root]; exists {
		return nil, fmt.Errorf("metrics: directory is already unlocked: %s", root)
	}
	marker := filepath.Join(root, keyFilename)
	data, err := os.ReadFile(marker)
	if err == nil {
		if len(data) != 8+12+len("metrics-encryption-key-v1")+aead.Overhead() || string(data[:8]) != magic {
			return nil, ErrInvalidKey
		}
		clear, err := aead.Open(nil, data[8:20], data[20:], []byte(magic))
		if err != nil || string(clear) != "metrics-encryption-key-v1" {
			return nil, ErrInvalidKey
		}
	} else if errors.Is(err, os.ErrNotExist) {
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, err
		}
		if len(entries) > 0 {
			return nil, ErrPlaintext
		}
		nonce := make([]byte, 12)
		if _, err := rand.Read(nonce); err != nil {
			return nil, err
		}
		m := append([]byte(magic), nonce...)
		m = aead.Seal(m, nonce, []byte("metrics-encryption-key-v1"), []byte(magic))
		f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(m); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	active.roots[root] = &Cipher{aead: aead}
	return func() { active.Lock(); delete(active.roots, root); active.Unlock() }, nil
}

// RequireKey prevents attempting to read an encrypted DB as plaintext.
func RequireKey(root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, keyFilename)); err == nil {
		return ErrLocked
	} else if errors.Is(err, os.ErrNotExist) {
		return nil
	} else {
		return err
	}
}

// encryptedNames lists small storage files without a .bin/.json extension
// that still carry series data or indexes and must be encrypted: the metric
// usage tracker, the composite-index marker, the per-part dedup marker and
// the hourly/daily metric-ID sets (opaque numeric IDs, encrypted to avoid any
// structural leak).
var encryptedNames = map[string]bool{
	"metric_usage_tracker":          true,
	"minTimestampForCompositeIndex": true,
	"min_dedup_interval":            true,
	"prev_hour_metric_ids_v2":       true,
	"curr_hour_metric_ids_v2":       true,
	"next_day_metric_ids_v3":        true,
}

func isProtectedName(name string) bool {
	// fs.MustWriteAtomic stages data in "name.tmp.N" with a numeric counter;
	// strip that suffix so atomic writes are encrypted before the rename.
	if i := strings.Index(name, ".tmp."); i > 0 {
		rest := name[i+len(".tmp."):]
		digits := len(rest) > 0
		for _, c := range rest {
			if c < '0' || c > '9' {
				digits = false
				break
			}
		}
		if digits {
			name = name[:i]
		}
	}
	if strings.HasSuffix(name, ".bin") || strings.HasSuffix(name, ".json") {
		return true
	}
	return encryptedNames[name]
}

func target(path string) (*Cipher, bool) {
	if !isProtectedName(filepath.Base(path)) {
		return nil, false
	}
	return rootFor(path)
}

// rootFor returns the cipher of the unlocked database containing path,
// regardless of the file name.
func rootFor(path string) (*Cipher, bool) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, false
	}
	active.RLock()
	defer active.RUnlock()
	for root, c := range active.roots {
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != "." && rel != ".." &&
			!strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return c, true
		}
	}
	return nil, false
}

func Enabled(path string) bool { _, ok := target(path); return ok }

// Encrypted reports whether path resides in an unlocked encrypted database,
// regardless of the file name. It decides persistence policy for files the
// envelope cannot cover, such as advisory cache directories.
func Encrypted(path string) bool { _, ok := rootFor(path); return ok }

func nonceFor(h []byte, n uint64) []byte {
	nonce := make([]byte, 12)
	copy(nonce, h[8:20])
	binary.LittleEndian.PutUint64(nonce[4:], binary.LittleEndian.Uint64(nonce[4:])+n)
	return nonce
}
func aad(h []byte, index uint64) []byte {
	p := make([]byte, headerSize+8)
	copy(p, h)
	binary.LittleEndian.PutUint64(p[headerSize:], index)
	return p
}

// Writer receives bytes from the storage engine *after* zstd compression,
// encrypts each 64KiB segment and writes only ciphertext to the file.
type Writer struct {
	f         *os.File
	aead      cipher.AEAD
	header    [headerSize]byte
	buf       []byte
	n         uint64
	written   uint64
	finished  bool
	finishErr error
}

func NewWriter(f *os.File) (*Writer, error) {
	c, ok := target(f.Name())
	if !ok {
		return nil, errors.New("cryptofs: file isn't in an unlocked database")
	}
	w := &Writer{f: f, aead: c.aead, buf: make([]byte, 0, blockSize)}
	copy(w.header[:8], magic)
	if _, err := rand.Read(w.header[8:20]); err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint32(w.header[20:24], blockSize)
	if _, err := f.Write(w.header[:]); err != nil {
		return nil, err
	}
	return w, nil
}
func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}
func (w *Writer) flushChunk(p []byte) error {
	if w.n == ^uint64(0) {
		return errors.New("cryptofs: chunk number exhausted")
	}
	ct := w.aead.Seal(nil, nonceFor(w.header[:], w.n), p, aad(w.header[:], w.n))
	if err := writeAll(w.f, ct); err != nil {
		return err
	}
	w.n++
	return nil
}
func (w *Writer) Write(p []byte) (int, error) {
	if w.finished {
		return 0, errors.New("cryptofs: writing after finish")
	}
	length := len(p)
	for len(p) > 0 {
		n := min(len(p), blockSize-len(w.buf))
		w.buf = append(w.buf, p[:n]...)
		p = p[n:]
		w.written += uint64(n)
		if len(w.buf) == blockSize {
			if err := w.flushChunk(w.buf); err != nil {
				return length - len(p), err
			}
			w.buf = w.buf[:0]
		}
	}
	return length, nil
}

// Finish authenticates the logical length, preventing undetected truncation
// of the final encrypted chunk(s). Caller must fsync and close the file.
func (w *Writer) Finish() error {
	if w.finished {
		return w.finishErr
	}
	if len(w.buf) > 0 {
		if err := w.flushChunk(w.buf); err != nil {
			return err
		}
		w.buf = w.buf[:0]
	}
	var size [8]byte
	binary.LittleEndian.PutUint64(size[:], w.written)
	t := w.aead.Seal(nil, nonceFor(w.header[:], ^uint64(0)), size[:], aad(w.header[:], ^uint64(0)))
	w.buf = nil
	// A failed trailer write cannot be retried (bytes may be partial), so
	// poison the writer: repeat Finish calls must report the same failure
	// instead of a false success.
	if err := writeAll(w.f, t); err != nil {
		w.finished = true
		w.finishErr = err
		return err
	}
	w.finished = true
	return nil
}

type Reader struct {
	f          *os.File
	aead       cipher.AEAD
	header     [headerSize]byte
	length     int64
	pos        int64
	mu         sync.Mutex
	cacheBlock uint64
	cache      []byte
	cacheReady bool
}

func NewReader(f *os.File) (*Reader, error) {
	c, ok := target(f.Name())
	if !ok {
		return nil, errors.New("cryptofs: file isn't in an unlocked database")
	}
	r := &Reader{f: f, aead: c.aead}
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() < headerSize+trailerSize {
		return nil, errors.New("cryptofs: short encrypted file")
	}
	if _, err := f.ReadAt(r.header[:], 0); err != nil {
		return nil, err
	}
	if string(r.header[:8]) != magic || binary.LittleEndian.Uint32(r.header[20:24]) != blockSize {
		return nil, errors.New("cryptofs: invalid header (unencrypted or damaged storage)")
	}
	t := make([]byte, trailerSize)
	if _, err := f.ReadAt(t, fi.Size()-trailerSize); err != nil {
		return nil, err
	}
	size, err := r.aead.Open(nil, nonceFor(r.header[:], ^uint64(0)), t, aad(r.header[:], ^uint64(0)))
	if err != nil {
		return nil, fmt.Errorf("cryptofs: file length authentication failed: %w", err)
	}
	if len(size) != 8 {
		return nil, errors.New("cryptofs: invalid file length")
	}
	total := binary.LittleEndian.Uint64(size)
	if total > uint64(^uint64(0)>>1) {
		return nil, errors.New("cryptofs: file too large")
	}
	r.length = int64(total)
	full := total / blockSize
	rem := total % blockSize
	expected := uint64(headerSize+trailerSize) + full*uint64(blockSize+16)
	if rem != 0 {
		expected += rem + 16
	}
	if expected != uint64(fi.Size()) {
		return nil, errors.New("cryptofs: encrypted file size mismatch")
	}
	return r, nil
}
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	r, err := NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return r, nil
}
func (r *Reader) Size() int64  { return r.length }
func (r *Reader) Close() error { return r.f.Close() }

func (r *Reader) readBlock(idx uint64) ([]byte, error) {
	if r.cacheReady && r.cacheBlock == idx {
		return r.cache, nil
	}
	off := int64(headerSize) + int64(idx)*int64(blockSize+16)
	blockOffset := idx * blockSize
	length := uint64(blockSize)
	if remainder := uint64(r.length) - blockOffset; remainder < length {
		length = remainder
	}
	ct := make([]byte, length+16)
	if _, err := r.f.ReadAt(ct, off); err != nil {
		return nil, err
	}
	clear, err := r.aead.Open(nil, nonceFor(r.header[:], idx), ct, aad(r.header[:], idx))
	if err != nil {
		return nil, fmt.Errorf("cryptofs: block %d authentication failed: %w", idx, err)
	}
	r.cache = clear
	r.cacheBlock = idx
	r.cacheReady = true
	return clear, nil
}

// ReadAt decrypts individual compressed chunks for VictoriaLogs random access.
func (r *Reader) ReadAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 {
		return 0, errors.New("cryptofs: negative offset")
	}
	if off >= r.length {
		return 0, io.EOF
	}
	target := len(p)
	if int64(target) > r.length-off {
		target = int(r.length - off)
	}
	done := 0
	for done < target {
		idx := uint64(off / int64(blockSize))
		data, err := r.readBlock(idx)
		if err != nil {
			return done, err
		}
		start := int(off % int64(blockSize))
		n := copy(p[done:target], data[start:])
		if n == 0 {
			return done, io.ErrUnexpectedEOF
		}
		done += n
		off += int64(n)
	}
	if done < len(p) {
		return done, io.EOF
	}
	return done, nil
}
func (r *Reader) Read(p []byte) (int, error) {
	r.mu.Lock()
	off := r.pos
	r.mu.Unlock()
	n, err := r.ReadAt(p, off)
	r.mu.Lock()
	r.pos += int64(n)
	r.mu.Unlock()
	return n, err
}
func (r *Reader) Seek(offset int64, whence int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += r.pos
	case io.SeekEnd:
		offset += r.length
	default:
		return 0, errors.New("cryptofs: invalid seek mode")
	}
	if offset < 0 {
		return 0, errors.New("cryptofs: negative seek")
	}
	r.pos = offset
	return offset, nil
}
func Size(path string) (uint64, error) {
	r, err := Open(path)
	if err != nil {
		return 0, err
	}
	defer r.Close()
	return uint64(r.Size()), nil
}
func ReadFile(path string) ([]byte, error) {
	if !Enabled(path) {
		return os.ReadFile(path)
	}
	r, err := Open(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if r.Size() > 128<<20 {
		return nil, errors.New("cryptofs: metadata too large")
	}
	b := make([]byte, r.Size())
	_, err = io.ReadFull(r, b)
	return b, err
}
