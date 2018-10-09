package io

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/kuberlab/pluk/pkg/types"
	"github.com/kuberlab/pluk/pkg/utils"
)

type PlukClient interface {
	CheckChunk(hash string) (*types.ChunkCheck, error)
	CheckChunkWebsocket(hash string) (res *types.ChunkCheck, err error)
	CheckDataset(workspace, dataset string) (*types.Dataset, error)
	CheckWorkspace(workspace string) (*types.Workspace, error)
	Close() error
	DeleteDataset(workspace, name string, force bool) error
	DeleteVersion(workspace, name, version string) error
	DownloadChunk(hash string) (io.ReadCloser, error)
	DownloadDataset(workspace, name, version string, w io.Writer) error
	DatasetTarsize(workspace, name, version string) (int64, error)
	GetFSStructure(workspace, name, version string) (*ChunkedFileFS, error)
	ListDatasets(workspace string) (*types.DataSetList, error)
	ListVersions(workspace, datasetName string) (*types.VersionList, error)
	PrepareWebsocket() error
	SaveChunk(hash string, data []byte) error
	SaveChunkWebsocket(hash string, data []byte) error
	SaveFileStructure(structure types.FileStructure, workspace, name, version string, create bool, publish bool) error
	WebdavAuth(user, pass, path string) (bool, error)
}

var MasterClient PlukClient

type ChunkedFileFS struct {
	lock    sync.RWMutex
	Root    string                    `json:"root"`
	Dirs    map[string]*ChunkedFileFS `json:"dirs"`  // Only dirs for current root
	Files   map[string]*ChunkedFile   `json:"files"` // Only files for current root
	FileObj *ChunkedFile              `json:"file_obj"`
}

func (fs *ChunkedFileFS) GetFile(absname string) *ChunkedFile {
	absname = strings.TrimPrefix(absname, "/")
	dirname := filepath.Dir(absname)
	filename := filepath.Base(absname)

	if absname == "" {
		return fs.FileObj
	}
	curDir := fs.GetDir(dirname)
	if curDir == nil {
		return nil
	}
	if f, ok := curDir.Files[filename]; ok {
		return f
	} else {
		if dir, ok := curDir.Dirs[filename]; ok {
			// Return file-dir object
			return dir.FileObj
		} else {
			return nil
		}
	}

	return nil
}

func (fs *ChunkedFileFS) dirObj(absname string) *ChunkedFile {
	return &ChunkedFile{
		Fstat: &ChunkedFileInfo{
			Fsize:    4096,
			Dir:      true,
			Fname:    filepath.Base(absname),
			Fmode:    0775,
			FmodTime: time.Now().Add(-time.Hour),
		},
		Size: 4096,
		Name: absname,
	}
}

func (fs *ChunkedFileFS) dirObjDate(absname string, dt time.Time) *ChunkedFile {
	dir := fs.dirObj(absname)
	dir.Fstat.FmodTime = dt
	return dir
}

func (fs *ChunkedFileFS) GetDir(dirname string) *ChunkedFileFS {
	if dirname == fs.Root || dirname == "." {
		return fs
	}

	dirname = strings.TrimPrefix(dirname, "/")
	splitted := strings.Split(dirname, "/")
	curDir := fs
	if dirname == "" {
		return curDir
	}
	for _, dir := range splitted {
		newDir, ok := curDir.Dirs[dir]
		if !ok {
			return nil
		}
		curDir = newDir
	}
	return curDir
}

func (fs *ChunkedFileFS) Walk(root string, walkFunc func(path string, f *ChunkedFile, err error) error) error {
	rootDir := fs.GetDir(root)
	if err := walkFunc(root, rootDir.FileObj, nil); err != nil {
		return err
	}
	if rootDir == nil {
		return nil
	}
	for _, d := range rootDir.Dirs {
		if err := d.Walk(d.Root, walkFunc); err != nil {
			return err
		}
	}
	for _, f := range fs.Files {
		if err := walkFunc(filepath.Join(rootDir.Root, f.Name), f, nil); err != nil {
			return err
		}
	}
	return nil
}

func (fs *ChunkedFileFS) Prepare() {
	// For reflective calls
	//for _, f := range fs.FS {
	//	f.fs = fs
	//}
}

func (fs *ChunkedFileFS) AddDir(path string, date time.Time) {
	base := filepath.Base(path)
	_, ok := fs.Dirs[base]

	if !ok {
		fs.Dirs[base] = &ChunkedFileFS{
			Root:  path,
			Dirs:  make(map[string]*ChunkedFileFS),
			Files: make(map[string]*ChunkedFile),
			//FmodTime: date,
			FileObj: &ChunkedFile{
				Fstat: &ChunkedFileInfo{
					Fname:    base,
					FmodTime: date,
					Dir:      true,
					Fmode:    0775,
					Fsize:    4096,
				},
				Size: 4096,
				Name: base,
			},
		}
	}
}

func (fs *ChunkedFileFS) Clone() *ChunkedFileFS {
	cloned := &ChunkedFileFS{
		Files:   make(map[string]*ChunkedFile),
		Dirs:    make(map[string]*ChunkedFileFS),
		Root:    fs.Root,
		FileObj: fs.FileObj,
	}
	for k, f := range fs.Files {
		cloned.Files[k] = &ChunkedFile{
			Name:               f.Name,
			currentChunkReader: nil,
			currentChunk:       0,
			Chunks:             f.Chunks,
			Fstat:              f.Fstat,
			fs:                 cloned,
			offset:             0,
			Ref:                f.Ref,
			Size:               f.Size,
		}
	}
	for k, d := range fs.Dirs {
		cloned.Dirs[k] = d.Clone()
	}
	return cloned
}

func (fs *ChunkedFileFS) Readdir(prefix string, count int) ([]os.FileInfo, error) {
	prefix = strings.TrimPrefix(prefix, "/")

	dir := fs.GetDir(prefix)
	if dir == nil {
		return nil, fmt.Errorf("No such directory: %v", prefix)
	}
	res := make([]os.FileInfo, 0)

	// Add all files and dirs within current directory
	for _, d := range dir.Dirs {
		res = append(res, d.FileObj.Fstat)
	}
	for _, f := range dir.Files {
		res = append(res, f.Fstat)
	}

	if count == 0 {
		count = len(res)
	}
	result := FileInfos(res[:count])
	sort.Sort(result)
	return result, nil
}

type ChunkedFile struct {
	Ref                string  `json:"ref"`
	Name               string  `json:"name"`
	Size               int64   `json:"size"`
	Chunks             []Chunk `json:"chunks"`
	currentChunk       int
	currentChunkReader ReaderInterface
	offset             int64 // absolute offset
	chunkOffset        int64

	Fstat *ChunkedFileInfo `json:"stat"`
	fs    *ChunkedFileFS
	lock  sync.RWMutex
}

type FileInfos []os.FileInfo

func (cf FileInfos) Len() int {
	return len(cf)
}

func (cf FileInfos) Less(i, j int) bool {
	cfi := cf[i]
	cfj := cf[j]
	nameFirst := cfi.Name() < cfj.Name()
	if cfi.IsDir() != cfj.IsDir() {
		return cfi.IsDir()
	} else {
		return nameFirst
	}
}

func (cf FileInfos) Swap(i, j int) {
	cf[i], cf[j] = cf[j], cf[i]
}

type Chunk struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func (f *ChunkedFile) Close() error {
	if f.currentChunkReader != nil {
		return f.currentChunkReader.Close()
	}
	return nil
}

func (f *ChunkedFile) getChunkReader(chunkPath string) (reader ReaderInterface, err error) {
	hash := utils.GetHashFromPath(chunkPath)
	return GetChunk(hash)
}

func (f *ChunkedFile) Read(p []byte) (n int, err error) {
	//f.lock.Lock()
	//defer f.lock.Unlock()
	var read int
	var reader ReaderInterface
	if f.currentChunkReader == nil {
		if len(f.Chunks) == 0 {
			return 0, io.EOF
		}
		reader, err = f.getChunkReader(f.Chunks[f.currentChunk].Path)
		if err != nil {
			logrus.Error(err)
			return read, io.EOF
		}
		f.currentChunkReader = reader
	}

	var r int
	// Shift read position to current offset
	f.currentChunkReader.Seek(f.chunkOffset, io.SeekStart)
	//f.chunkOffset = 0
	chunk := f.currentChunk
	for {
		r, err = f.currentChunkReader.Read(p[read:])
		if f.currentChunk == chunk {
			f.chunkOffset += int64(r)
		}
		read += r
		if err == nil && read < len(p) {
			// Means that buffer is not yet full, but is not EOF as well.
			// Try read more, EOF should appear next.
			continue
		}
		if err == io.EOF && f.currentChunk < (len(f.Chunks)-1) && read < len(p) {
			// Read more; current chunk is over.
			f.currentChunkReader.Close()
			f.currentChunk++
			chunk = f.currentChunk
			f.chunkOffset = 0
			reader, err = f.getChunkReader(f.Chunks[f.currentChunk].Path)
			if err != nil {
				logrus.Error(err)
				f.currentChunkReader = nil
				return read, io.EOF
			}
			f.currentChunkReader = reader
			err = nil
		} else {
			// either nothing to read or
			// all chunks are over or
			// buffer is full
			if err == io.EOF && f.currentChunk >= len(f.Chunks)-1 {
				// Whole file EOF
				f.currentChunkReader.Close()
				f.currentChunk = 0
				f.chunkOffset = 0
				f.currentChunkReader = nil
				return read, io.EOF
			}
			break
		}
	}

	return read, err
}

func (f *ChunkedFile) SeekAndRead(p []byte, offset int64) (int, error) {
	f.lock.Lock()
	defer f.lock.Unlock()
	_, err := f.Seek(offset, io.SeekStart)
	if err != nil {
		return 0, err
	}
	return f.Read(p)
}

func (f *ChunkedFile) Seek(offset int64, whence int) (res int64, err error) {
	//f.lock.Lock()
	//defer f.lock.Unlock()
	if (whence == io.SeekStart && offset > f.Size) || (whence == io.SeekEnd && offset > 0) {
		return 0, fmt.Errorf("offset %v more than Size of the file", offset)
	}

	if whence == io.SeekStart && offset < 0 {
		return 0, fmt.Errorf("seek before the start of the file")
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

	prevCurrentChunk := f.currentChunk
	ofs := absoluteOffset
	for i, ch := range f.Chunks {
		if ofs-ch.Size < 0 {
			f.currentChunk = i
			f.chunkOffset = ofs
			break
		}
		ofs -= ch.Size
	}
	f.offset = absoluteOffset

	if f.currentChunkReader != nil && prevCurrentChunk != f.currentChunk {
		f.currentChunkReader.Close()
		f.currentChunkReader = nil
	}

	return absoluteOffset, nil
}

func (f *ChunkedFile) Readdir(count int) ([]os.FileInfo, error) {
	return f.fs.Readdir(f.Name, count)
}

func (f *ChunkedFile) Stat() (os.FileInfo, error) {
	return f.Fstat, nil
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
