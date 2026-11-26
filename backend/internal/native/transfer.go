package native

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
)

// StreamDownload opens virtualPath for streaming download directly to an HTTP client.
// Local files are strictly confined to fnOS authorized os.Root; remote files stream
// from `rclone cat`.
func (c Config) StreamDownload(ctx context.Context, virtualPath string, opts SessionOptions) (io.ReadCloser, string, int64, error) {
	v := strings.TrimRight(strings.TrimSpace(virtualPath), "/")
	if v == "" || v == "/" {
		return nil, "", 0, errors.New("请选择要下载的文件")
	}
	ep, err := c.ResolveEntry(v)
	if err != nil {
		return nil, "", 0, err
	}
	fileName := path.Base(v)
	if fileName == "" || fileName == "." || fileName == "/" {
		return nil, "", 0, errors.New("文件名无效")
	}
	if ep.IsLocal {
		_, sub := parts(v)
		root, rel, rootDev, hasRootDev, openErr := openDiskWithDev(sub)
		if openErr != nil {
			return nil, "", 0, openErr
		}
		if rel == "." || rel == "" {
			_ = root.Close()
			return nil, "", 0, errors.New("不支持直接下载根目录")
		}
		st, statErr := root.Lstat(rel)
		if statErr != nil {
			_ = root.Close()
			if errors.Is(statErr, os.ErrNotExist) {
				return nil, "", 0, errors.New("文件不存在")
			}
			return nil, "", 0, statErr
		}
		if st.IsDir() {
			_ = root.Close()
			return nil, "", 0, errors.New("暂不支持直接下载目录，请使用恢复到本地功能")
		}
		if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() || !sameDevice(st, rootDev, hasRootDev) {
			_ = root.Close()
			return nil, "", 0, errors.New("目标不是可下载的普通文件")
		}
		f, fErr := root.Open(rel)
		if fErr != nil {
			_ = root.Close()
			return nil, "", 0, fErr
		}
		return &rootFileCloser{File: f, root: root}, fileName, st.Size(), nil
	}

	name, _ := parts(v)
	s, err := c.SessionFor([]SessionSpec{{Name: name, Mode: diskReadOnly}}, opts)
	if err != nil {
		return nil, "", 0, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	cmd, err := s.Cmd(streamCtx, "cat", "--", ep.Path)
	if err != nil {
		cancel()
		s.Close()
		return nil, "", 0, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		s.Close()
		return nil, "", 0, err
	}
	errBuf := &tailBuffer{limit: maxStderrCapture}
	cmd.Stderr = errBuf
	if err := cmd.Start(); err != nil {
		cancel()
		_ = stdout.Close()
		s.Close()
		return nil, "", 0, err
	}
	return &rcloneStreamReader{
		rc:      stdout,
		cmd:     cmd,
		session: s,
		errBuf:  errBuf,
		backend: ep.Backend,
		cancel:  cancel,
	}, fileName, -1, nil
}

type rootFileCloser struct {
	*os.File
	root *os.Root
}

func (r *rootFileCloser) Close() error {
	err1 := r.File.Close()
	err2 := r.root.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

type rcloneStreamReader struct {
	rc        io.ReadCloser
	cmd       *exec.Cmd
	session   *Session
	errBuf    *tailBuffer
	backend   string
	cancel    context.CancelFunc
	finish    sync.Once
	finishErr error
}

func (r *rcloneStreamReader) Read(p []byte) (int, error) {
	n, err := r.rc.Read(p)
	if err == io.EOF {
		if finishErr := r.finishCommand(); finishErr != nil {
			return n, finishErr
		}
	}
	return n, err
}

func (r *rcloneStreamReader) Close() error {
	r.cancel()
	_ = r.rc.Close()
	return r.finishCommand()
}

func (r *rcloneStreamReader) finishCommand() error {
	r.finish.Do(func() {
		waitErr := r.cmd.Wait()
		r.cancel()
		r.session.Close()
		if waitErr == nil {
			return
		}
		if detail := strings.TrimSpace(r.errBuf.String()); detail != "" {
			r.finishErr = classifyRcloneError(r.backend, detail)
			return
		}
		r.finishErr = fmt.Errorf("远端下载进程失败：%w", waitErr)
	})
	return r.finishErr
}

// StreamUpload streams a single file upload from reader into dirVirtualPath/fileName.
// Local uploads write atomically inside os.Root; remote uploads stream via `rclone rcat`.
func (c Config) StreamUpload(ctx context.Context, dirVirtualPath, fileName string, reader io.Reader, opts SessionOptions) (int64, error) {
	dirV := strings.TrimRight(strings.TrimSpace(dirVirtualPath), "/")
	fileName = strings.TrimSpace(fileName)
	if dirV == "" || dirV == "/" {
		return 0, errors.New("请先进入具体的存储目录后再上传")
	}
	if fileName == "" || !cleanRelative(fileName) || strings.Contains(fileName, "/") || strings.Contains(fileName, `\`) || isInternalPartialEntry(fileName) {
		return 0, errors.New("上传文件名无效")
	}
	targetV := dirV + "/" + fileName
	ep, err := c.ResolveEntry(targetV)
	if err != nil {
		return 0, err
	}
	if ep.IsLocal {
		_, dirSub := parts(dirV)
		root, baseRel, openErr := openDisk(dirSub)
		if openErr != nil {
			return 0, openErr
		}
		defer root.Close()
		if err := mkdirAllPerm(root, baseRel, backupDirPerm); err != nil {
			return 0, err
		}
		var randBytes [6]byte
		_, _ = rand.Read(randBytes[:])
		tmpName := fmt.Sprintf(".opensync-upload-%s.partial", hex.EncodeToString(randBytes[:]))
		tmpRel := joinInRoot(baseRel, tmpName)
		targetRel := joinInRoot(baseRel, fileName)
		f, err := root.OpenFile(tmpRel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, backupFilePerm)
		if err != nil {
			return 0, err
		}
		n, copyErr := io.Copy(f, reader)
		if copyErr != nil {
			_ = f.Close()
			_ = root.Remove(tmpRel)
			return n, copyErr
		}
		_ = f.Chmod(backupFilePerm)
		if err := f.Sync(); err != nil {
			_ = f.Close()
			_ = root.Remove(tmpRel)
			return n, err
		}
		if err := f.Close(); err != nil {
			_ = root.Remove(tmpRel)
			return n, err
		}
		if err := root.Rename(tmpRel, targetRel); err != nil {
			_ = root.Remove(tmpRel)
			return n, err
		}
		return n, nil
	}

	name, _ := parts(targetV)
	s, err := c.SessionFor([]SessionSpec{{Name: name, Mode: diskAppendWrite}}, opts)
	if err != nil {
		return 0, err
	}
	defer s.Close()

	var written int64
	cr := &countingReader{r: reader, n: &written}
	cmd, err := s.Cmd(ctx, "rcat", "--", ep.Path)
	if err != nil {
		return 0, err
	}
	cmd.Stdin = cr
	errBuf := &tailBuffer{limit: maxStderrCapture}
	cmd.Stderr = errBuf
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return written, ctx.Err()
		}
		return written, classifyRcloneError(ep.Backend, errBuf.String())
	}
	return written, nil
}

type countingReader struct {
	r io.Reader
	n *int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	*c.n += int64(n)
	return n, err
}

// CopyURLWithProgress pulls an HTTP(S) URL into dstDir while streaming progress
// callbacks when the destination is local (HTTP Range resumable) or falling back
// to CopyURL for remote destinations.
func (c Config) CopyURLWithProgress(
	ctx context.Context,
	rawURL, dstDir string,
	opt CopyURLOptions,
	opts SessionOptions,
	onProgress func(doneBytes, totalBytes int64, speed float64),
) (string, error) {
	dstDir = strings.TrimRight(strings.TrimSpace(dstDir), "/")
	if ep, err := c.Resolve(dstDir); err == nil && ep.IsLocal {
		_, sub := parts(dstDir)
		return copyURLToLocalResumable(ctx, rawURL, sub, opt, opts, nil, onProgress)
	}
	return c.CopyURL(ctx, rawURL, dstDir, opt, opts)
}
