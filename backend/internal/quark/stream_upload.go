package quark

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"time"
)

const streamInMemoryRapidLimit int64 = 32 << 20 // 32 MiB

// UploadStream uploads an arbitrary io.Reader (e.g. WebDAV PUT from rclone
// crypt or remote-to-Quark copy) to relPath.
//
//   - Known-size streams <= 32 MiB are buffered in memory so MD5+SHA1 rapid upload
//     (秒传) can still be triggered.
//   - Known-size streams > 32 MiB stream directly into part_size OSS chunks with
//     bounded memory (no local temp file).
//   - Unknown-size streams (size < 0) are spooled to a private temporary file so
//     exact byte size and hashes are known before calling /file/upload/pre.
func (c *Client) UploadStream(ctx context.Context, relPath string, body io.Reader, size int64, modTime *time.Time) (*UploadResult, error) {
	clean := cleanRelPath(relPath)
	if clean == "" {
		return nil, ErrInvalidName
	}
	parentRel := path.Dir(clean)
	if parentRel == "." || parentRel == "/" {
		parentRel = ""
	}
	targetName := path.Base(clean)
	if !isSafeEntryName(targetName) {
		return nil, ErrInvalidName
	}

	var mt time.Time
	if modTime != nil {
		mt = *modTime
	}

	// Unknown-size stream: spool to a temporary file and reuse UploadFile (gains rapid upload + concurrency).
	if size < 0 {
		tmp, err := os.CreateTemp("", "opensync-quark-stream-*")
		if err != nil {
			return nil, err
		}
		tmpName := tmp.Name()
		defer func() {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}()
		written, err := io.Copy(tmp, body)
		if err != nil {
			return nil, err
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		return c.UploadFile(ctx, clean, UploadFileOptions{
			File:      tmp,
			Size:      written,
			ModTime:   mt,
			Overwrite: true,
		})
	}

	parentFID, err := c.MkdirAll(ctx, parentRel)
	if err != nil {
		return nil, err
	}

	var oldFID string
	if existing, statErr := c.Stat(ctx, clean); statErr == nil {
		if existing.IsDir {
			return nil, fmt.Errorf("目标路径 %q 已存在同名目录", clean)
		}
		oldFID = existing.FID
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}

	uploadName := targetName
	usingTempName := oldFID != ""
	if usingTempName {
		uploadName = ".opensync-up-" + randomHex(6) + "-" + targetName
	}
	mimeType := detectMimeType(targetName)

	// Small known-size stream: buffer in memory for MD5+SHA1 rapid upload.
	if size <= streamInMemoryRapidLimit {
		data, err := io.ReadAll(io.LimitReader(body, size+1))
		if err != nil {
			return nil, err
		}
		if int64(len(data)) != size {
			return nil, fmt.Errorf("流式上传长度不匹配（期望 %d 字节，实际 %d 字节）", size, len(data))
		}
		mSum := md5.Sum(data)
		sSum := sha1.Sum(data)
		md5Hex := hex.EncodeToString(mSum[:])
		sha1Hex := hex.EncodeToString(sSum[:])

		pre, err := c.upPre(ctx, parentFID, uploadName, mimeType, size, mt)
		if err != nil {
			return nil, err
		}
		if pre.Data.Finish {
			fid, err := c.finalizeOverwriteSwap(ctx, parentFID, parentRel, clean, targetName, uploadName, oldFID, pre.Data.FID, usingTempName)
			if err != nil {
				return nil, err
			}
			return &UploadResult{FID: fid, Name: targetName, Size: size, MD5: md5Hex, SHA1: sha1Hex, Rapid: true}, nil
		}
		hashResp, err := c.upHash(ctx, pre.Data.TaskID, md5Hex, sha1Hex)
		if err != nil {
			return nil, err
		}
		if hashResp.Data.Finish {
			fid := hashResp.Data.FID
			if fid == "" {
				fid = pre.Data.FID
			}
			fid, err = c.finalizeOverwriteSwap(ctx, parentFID, parentRel, clean, targetName, uploadName, oldFID, fid, usingTempName)
			if err != nil {
				return nil, err
			}
			return &UploadResult{FID: fid, Name: targetName, Size: size, MD5: md5Hex, SHA1: sha1Hex, Rapid: true}, nil
		}

		partSize := pre.Metadata.PartSize
		if partSize <= 0 {
			partSize = DefaultPartSize
		}
		fmtType := pre.Data.FormatType
		if fmtType == "" {
			fmtType = mimeType
		}
		sess := uploadSessionParams{
			TaskID:     pre.Data.TaskID,
			UploadID:   pre.Data.UploadID,
			ObjKey:     pre.Data.ObjKey,
			UploadURL:  pre.Data.UploadURL,
			Bucket:     pre.Data.Bucket,
			AuthInfo:   pre.Data.AuthInfo,
			FormatType: fmtType,
			Callback:   pre.Data.Callback,
			PartSize:   partSize,
		}
		etags := map[int]string{}
		totalParts := 1
		if size > 0 {
			totalParts = int((size + partSize - 1) / partSize)
		}
		for partNum := 1; partNum <= totalParts; partNum++ {
			off := int64(partNum-1) * partSize
			end := off + partLength(size, partSize, partNum)
			etag, err := c.uploadPartBytes(ctx, sess, partNum, data[off:end], nil, nil, nil)
			if err != nil {
				return nil, err
			}
			etags[partNum] = etag
		}
		if err := c.upCommit(ctx, sess, etags); err != nil {
			return nil, err
		}
		finalFID, err := c.upFinish(ctx, sess.ObjKey, sess.TaskID)
		if err != nil {
			return nil, err
		}
		finalFID, err = c.finalizeOverwriteSwap(ctx, parentFID, parentRel, clean, targetName, uploadName, oldFID, finalFID, usingTempName)
		if err != nil {
			return nil, err
		}
		return &UploadResult{FID: finalFID, Name: targetName, Size: size, MD5: md5Hex, SHA1: sha1Hex, Rapid: false}, nil
	}

	// Large known-size stream (> 32 MiB): chunk-stream directly without full buffering.
	pre, err := c.upPre(ctx, parentFID, uploadName, mimeType, size, mt)
	if err != nil {
		return nil, err
	}
	partSize := pre.Metadata.PartSize
	if partSize <= 0 {
		partSize = DefaultPartSize
	}
	fmtType := pre.Data.FormatType
	if fmtType == "" {
		fmtType = mimeType
	}
	sess := uploadSessionParams{
		TaskID:     pre.Data.TaskID,
		UploadID:   pre.Data.UploadID,
		ObjKey:     pre.Data.ObjKey,
		UploadURL:  pre.Data.UploadURL,
		Bucket:     pre.Data.Bucket,
		AuthInfo:   pre.Data.AuthInfo,
		FormatType: fmtType,
		Callback:   pre.Data.Callback,
		PartSize:   partSize,
	}

	buf := make([]byte, partSize)
	hMD5 := md5.New()
	hSHA1 := sha1.New()
	etags := map[int]string{}
	var remaining = size
	partNum := 1
	for remaining > 0 {
		c.MaybeKeepalive(ctx)
		want := partSize
		if want > remaining {
			want = remaining
		}
		n, err := io.ReadFull(body, buf[:want])
		if err != nil {
			return nil, fmt.Errorf("读取流式上传数据失败（分片 #%d）: %w", partNum, err)
		}
		chunk := buf[:n]
		_, _ = hMD5.Write(chunk)
		_, _ = hSHA1.Write(chunk)
		etag, err := c.uploadPartBytes(ctx, sess, partNum, chunk, nil, nil, nil)
		if err != nil {
			return nil, err
		}
		etags[partNum] = etag
		remaining -= int64(n)
		partNum++
	}
	if err := c.upCommit(ctx, sess, etags); err != nil {
		return nil, err
	}
	finalFID, err := c.upFinish(ctx, sess.ObjKey, sess.TaskID)
	if err != nil {
		return nil, err
	}
	finalFID, err = c.finalizeOverwriteSwap(ctx, parentFID, parentRel, clean, targetName, uploadName, oldFID, finalFID, usingTempName)
	if err != nil {
		return nil, err
	}
	return &UploadResult{
		FID:   finalFID,
		Name:  targetName,
		Size:  size,
		MD5:   hex.EncodeToString(hMD5.Sum(nil)),
		SHA1:  hex.EncodeToString(hSHA1.Sum(nil)),
		Rapid: false,
	}, nil
}
