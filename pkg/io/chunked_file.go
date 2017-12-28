package io

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kuberlab/pacak/pkg/pacakimpl"
	"golang.org/x/net/webdav"
)

type ChunkedFileFS struct {
	FS map[string]*ChunkedFile
}

func (fs *ChunkedFileFS) Readdir(prefix string, count int) ([]os.FileInfo, error) {
	// filter infos by prefix.
	res := make([]os.FileInfo, 0)
	for _, f := range fs.FS {
		if strings.HasPrefix(f.Name, prefix) && f.Name != prefix {
			path := strings.TrimPrefix(f.Name, prefix)
			if strings.Contains(path, "/") {
				continue
			}

			res = append(res, f.Fstat)
		}
	}

	if count == 0 {
		count = len(res)
	}
	return res[:count], nil
}

type ChunkedFile struct {
	//repo               pacakimpl.PacakRepo
	Ref                string  `json:"ref"`
	Name               string  `json:"name"`
	Size               int64   `json:"size"`
	Chunks             []chunk `json:"chunks"`
	currentChunk       int
	currentChunkReader io.ReadCloser
	offset             int64 // absolute offset

	Fstat *ChunkedFileInfo `json:"stat"`
	fs    *ChunkedFileFS
}

type chunk struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func NewInternalChunked(repo pacakimpl.PacakRepo, ref, path string) (*ChunkedFile, error) {
	chunked, err := NewChunkedFileFromRepo(repo, ref, path)
	if err != nil {
		return nil, err
	}
	return chunked.(*ChunkedFile), nil
}

func NewChunkedFileFromRepo(repo pacakimpl.PacakRepo, ref, path string) (webdav.File, error) {
	file := &ChunkedFile{Name: path, Ref: ref}

	if path == "/" {
		return file, nil
	}
	data, err := repo.GetFileDataAtRev(ref, path)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) < 2 {
		return file, nil
		//return nil, fmt.Errorf("Probably corrupted file [Name=%v], contained less than 2 lines: %v", f.Name(), string(data))
	}

	size, err := strconv.ParseInt(lines[0], 10, 64)
	if err != nil {
		return file, nil
		//return nil, err
	}

	file.Size = size
	file.Chunks = make([]chunk, 0)
	for _, chunkPath := range lines[1:] {
		info, err := os.Stat(chunkPath)
		if err != nil {
			return nil, err
		}
		file.Chunks = append(file.Chunks, chunk{chunkPath, info.Size()})
	}
	//file.Dir = Path.Dir(f.Name())
	return file, nil
}

func (f *ChunkedFile) Close() error {
	if f.currentChunkReader != nil {
		return f.currentChunkReader.Close()
	}
	return nil
}

func (f *ChunkedFile) Read(p []byte) (n int, err error) {
	var read int
	if f.currentChunkReader == nil {
		if len(f.Chunks) == 0 {
			return 0, io.EOF
		}
		reader, err := os.Open(f.Chunks[f.currentChunk].Path)
		if err != nil {
			return read, err
		}
		f.currentChunkReader = reader
	}

	var reader *os.File
	var r int
	for {
		r, err = f.currentChunkReader.Read(p[read:])
		read += r
		if err == io.EOF && f.currentChunk < (len(f.Chunks)-1) && read < len(p) {
			// Read more; current chunk is over.
			f.currentChunkReader.Close()
			f.currentChunk++
			reader, err = os.Open(f.Chunks[f.currentChunk].Path)
			if err != nil {
				return read, err
			}
			f.currentChunkReader = reader
			err = nil
		} else {
			// either nothing to read or
			// all chunks are over or
			// buffer is full
			break
		}
	}

	return read, err
}

func (f *ChunkedFile) Seek(offset int64, whence int) (res int64, err error) {
	if (whence == io.SeekStart && offset > f.Size) || (whence == io.SeekEnd && offset > 0) {
		return 0, fmt.Errorf("offset %v more than Size of the file", offset)
	}

	if whence == io.SeekStart && offset < 0 {
		return 0, fmt.Errorf("seek before the start of the file")
	}

	if f.currentChunkReader != nil {
		f.currentChunkReader.Close()
		f.currentChunkReader = nil
	}

	var absoluteOffset int64
	switch whence {
	case io.SeekStart:
		absoluteOffset = offset
	case io.SeekCurrent:
		absoluteOffset = f.offset + offset
	case io.SeekEnd:
		absoluteOffset = f.Size - offset
	}

	ofs := absoluteOffset
	for i, ch := range f.Chunks {
		if ofs-ch.Size < 0 {
			f.currentChunk = i
			break
		}
		ofs -= ch.Size
	}
	f.offset = absoluteOffset

	return absoluteOffset, nil
}

func (f *ChunkedFile) Readdir(count int) ([]os.FileInfo, error) {
	return f.fs.Readdir(f.Name, count)
}

func (f *ChunkedFile) Stat() (os.FileInfo, error) {
	return f.Fstat, nil

	//t := time.Now()
	//baseStat, err := f.repo.StatFileAtRev(f.Ref, f.Name)
	//if err != nil {
	//	return nil, err
	//}
	//info := &ChunkedFileInfo{
	//	Fmode:    baseStat.Mode(),
	//	FmodTime: baseStat.ModTime(),
	//	Fname:    baseStat.Name(),
	//	Dir:      baseStat.IsDir(),
	//}
	//if baseStat.IsDir() {
	//	info.Fsize = 4096
	//} else {
	//	info.Fsize = f.Size
	//}
	//
	////fmt.Println("STAT", f.Name, time.Since(t), *info)
	//return info, nil
}

func (*ChunkedFile) Write(p []byte) (int, error) {
	return 0, errors.New("Read only")
}

// A ChunkedFileInfo is the implementation of FileInfo returned by Stat and Lstat.
type ChunkedFileInfo struct {
	Dir      bool        `json:"dir"`
	Fname    string      `json:"name"`
	Fsize    int64       `json:"size"`
	Fmode    os.FileMode `json:"mode"`
	FmodTime time.Time   `json:"modtime"`
}

func (fs *ChunkedFileInfo) Name() string       { return fs.Fname }
func (fs *ChunkedFileInfo) IsDir() bool        { return fs.Dir }
func (fs *ChunkedFileInfo) Size() int64        { return fs.Fsize }
func (fs *ChunkedFileInfo) Mode() os.FileMode  { return fs.Fmode }
func (fs *ChunkedFileInfo) ModTime() time.Time { return fs.FmodTime }
func (fs *ChunkedFileInfo) Sys() interface{}   { return nil }
