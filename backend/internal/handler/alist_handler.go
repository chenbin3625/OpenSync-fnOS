package handler

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"opensync/internal/msg"
	"opensync/internal/service"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// GetAlist handles GET /svr/alist
func GetAlist(c *gin.Context) {
	// Check if it's a path select request
	alistIDStr := c.Query("alistId")
	path := c.Query("path")
	if path != "" {
		var alistID int64
		if alistIDStr != "" {
			var err error
			alistID, err = parseRequiredID(alistIDStr)
			if err != nil {
				respondError(c, err.Error())
				return
			}
		}
		handleService(c, func() (interface{}, error) {
			return service.GetChildPath(c.Request.Context(), alistID, path)
		})
		return
	}
	// Return client list
	handleService(c, func() (interface{}, error) {
		return service.GetClientList()
	})
}

func GetNativeStorage(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	handleService(c, func() (interface{}, error) {
		return service.GetNativeStorageState(c.Request.Context())
	})
}

func SaveNativeEngine(c *gin.Context) {
	var in service.NativeEngineInput
	if !bindJSONStrict(c, &in) {
		return
	}
	handleService(c, func() (interface{}, error) {
		return service.SaveNativeEngine(c.Request.Context(), in)
	})
}

func SaveNativeStorage(c *gin.Context) {
	var in service.NativeStorageInput
	if !bindJSONStrict(c, &in) {
		return
	}
	handleServiceVoid(c, func() error { return service.SaveNativeStorage(c.Request.Context(), in) })
}

func UpdateNativeStorage(c *gin.Context) {
	var in service.NativeRemoteEditInput
	if !bindJSONStrict(c, &in) {
		return
	}
	handleServiceVoid(c, func() error { return service.UpdateNativeStorage(c.Request.Context(), in) })
}

func PreviewNativeImport(c *gin.Context) {
	var in service.NativeImportInput
	if !bindJSONStrict(c, &in) {
		return
	}
	handleService(c, func() (interface{}, error) {
		return service.PreviewNativeImport(c.Request.Context(), in)
	})
}

func ImportNativeStorage(c *gin.Context) {
	var in service.NativeImportInput
	if !bindJSONStrict(c, &in) {
		return
	}
	handleService(c, func() (interface{}, error) {
		return service.ImportNativeStorage(c.Request.Context(), in)
	})
}

func TestNativeStorage(c *gin.Context) {
	var in service.NativeStorageTestInput
	if !bindJSONStrict(c, &in) {
		return
	}
	handleService(c, func() (interface{}, error) {
		return service.TestNativeStorage(c.Request.Context(), in)
	})
}

func DeleteNativeStorage(c *gin.Context) {
	name := c.Query("name")
	if name == "" {
		respondError(c, msg.T(msg.LostPart))
		return
	}
	handleServiceVoid(c, func() error { return service.DeleteNativeStorage(name) })
}

func RenameNativeStorage(c *gin.Context) {
	var in service.NativeRenameInput
	if !bindJSONStrict(c, &in) {
		return
	}
	handleService(c, func() (interface{}, error) {
		return service.RenameNativeStorage(c.Request.Context(), in)
	})
}

func ExportNativeConfig(c *gin.Context) {
	var in service.NativeExportInput
	if !bindJSONStrict(c, &in) {
		return
	}
	c.Header("Cache-Control", "no-store")
	handleService(c, func() (interface{}, error) {
		return service.ExportNativeConfig(c.Request.Context(), in)
	})
}

func AboutNativeStorage(c *gin.Context) {
	var in service.NativeAboutInput
	if !bindJSONStrict(c, &in) {
		return
	}
	c.Header("Cache-Control", "no-store")
	handleService(c, func() (interface{}, error) {
		return service.AboutNativeStorage(c.Request.Context(), in)
	})
}

func BrowseNativeStorage(c *gin.Context) {
	var in service.NativeBrowseInput
	if !bindJSONStrict(c, &in) {
		return
	}
	c.Header("Cache-Control", "no-store")
	handleService(c, func() (interface{}, error) {
		return service.BrowseNativeStorage(c.Request.Context(), in)
	})
}

func MkdirNativeStorage(c *gin.Context) {
	var in service.NativePathInput
	if !bindJSONStrict(c, &in) {
		return
	}
	handleServiceVoid(c, func() error {
		return service.MkdirNativeStorage(c.Request.Context(), in)
	})
}

func MoveNativeStoragePath(c *gin.Context) {
	var in service.NativeMovePathInput
	if !bindJSONStrict(c, &in) {
		return
	}
	handleServiceVoid(c, func() error {
		return service.MoveNativeStoragePath(c.Request.Context(), in)
	})
}

func DeleteNativeStoragePath(c *gin.Context) {
	var in service.NativeDeletePathInput
	if !bindJSONStrict(c, &in) {
		return
	}
	handleServiceVoid(c, func() error {
		return service.DeleteNativeStoragePath(c.Request.Context(), in)
	})
}

func SizeNativeStorage(c *gin.Context) {
	var in service.NativePathInput
	if !bindJSONStrict(c, &in) {
		return
	}
	c.Header("Cache-Control", "no-store")
	handleService(c, func() (interface{}, error) {
		return service.SizeNativeStorage(c.Request.Context(), in)
	})
}

func StartNativeStorageAuth(c *gin.Context) {
	var in service.NativeStorageAuthStartInput
	if !bindJSONStrict(c, &in) {
		return
	}
	c.Header("Cache-Control", "no-store")
	handleService(c, func() (interface{}, error) {
		return service.StartNativeStorageAuth(c.Request.Context(), in)
	})
}

func GetNativeStorageAuth(c *gin.Context) {
	sessionID := c.Query("sessionId")
	if sessionID == "" {
		respondError(c, msg.T(msg.LostPart))
		return
	}
	c.Header("Cache-Control", "no-store")
	handleService(c, func() (interface{}, error) {
		return service.GetNativeStorageAuth(sessionID)
	})
}

func SubmitNativeStorageAuthCallback(c *gin.Context) {
	var in service.NativeStorageAuthCallbackInput
	if !bindJSONStrict(c, &in) {
		return
	}
	c.Header("Cache-Control", "no-store")
	handleService(c, func() (interface{}, error) {
		return service.SubmitNativeStorageAuthCallback(c.Request.Context(), in)
	})
}

func ContinueNativeStorageAuth(c *gin.Context) {
	var in service.NativeStorageAuthContinueInput
	if !bindJSONStrict(c, &in) {
		return
	}
	c.Header("Cache-Control", "no-store")
	handleService(c, func() (interface{}, error) {
		return service.ContinueNativeStorageAuth(c.Request.Context(), in)
	})
}

func CancelNativeStorageAuth(c *gin.Context) {
	sessionID := c.Query("sessionId")
	if sessionID == "" {
		respondError(c, msg.T(msg.LostPart))
		return
	}
	handleServiceVoid(c, func() error {
		return service.CancelNativeStorageAuth(sessionID)
	})
}

func ListNativeTransfers(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	respondOK(c, service.ListNativeTransfers())
}

func StartAsyncRestoreNativeFile(c *gin.Context) {
	var in service.NativeRestoreInput
	if !bindJSONStrict(c, &in) {
		return
	}
	handleService(c, func() (interface{}, error) {
		return service.StartAsyncRestoreNativeFile(in)
	})
}

func StartBatchNativeStorageAction(c *gin.Context) {
	var in service.NativeBatchInput
	if !bindJSONStrict(c, &in) {
		return
	}
	handleService(c, func() (interface{}, error) {
		return service.StartBatchNativeStorageAction(in)
	})
}

func CancelOrClearNativeTransfers(c *gin.Context) {
	if c.Query("completed") == "1" {
		service.ClearCompletedNativeTransfers()
		respondOK(c, nil)
		return
	}
	id := strings.TrimSpace(c.Query("id"))
	if id == "" {
		respondError(c, msg.T(msg.LostPart))
		return
	}
	handleServiceVoid(c, func() error {
		return service.CancelNativeTransfer(id)
	})
}

func BrowseNativeTrash(c *gin.Context) {
	var in service.NativeTrashBrowseInput
	if !bindJSONStrict(c, &in) {
		return
	}
	c.Header("Cache-Control", "no-store")
	handleService(c, func() (interface{}, error) {
		return service.BrowseNativeTrash(c.Request.Context(), in)
	})
}

func RestoreNativeTrashItem(c *gin.Context) {
	var in service.NativeTrashRestoreInput
	if !bindJSONStrict(c, &in) {
		return
	}
	handleServiceVoid(c, func() error {
		return service.RestoreNativeTrashItem(c.Request.Context(), in)
	})
}

func DownloadNativeStorageFile(c *gin.Context) {
	vPath := strings.TrimSpace(c.Query("path"))
	if vPath == "" {
		respondError(c, msg.T(msg.LostPart))
		return
	}
	rc, fileName, size, err := service.OpenNativeStorageDownload(c.Request.Context(), vPath)
	if err != nil {
		respondServiceError(c, err)
		return
	}
	safeASCII := strings.Map(func(r rune) rune {
		if r >= 0x20 && r <= 0x7e && r != '"' && r != '\\' && r != ';' {
			return r
		}
		return '_'
	}, fileName)
	if safeASCII == "" {
		safeASCII = "download"
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, safeASCII, url.PathEscape(fileName)))
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Cache-Control", "no-store")
	if size >= 0 {
		c.Header("Content-Length", strconv.FormatInt(size, 10))
	}
	written, streamErr := io.Copy(c.Writer, rc)
	if closeErr := rc.Close(); streamErr == nil {
		streamErr = closeErr
	}
	if streamErr == nil && size >= 0 && written != size {
		streamErr = io.ErrUnexpectedEOF
	}
	if streamErr == nil {
		if !c.Writer.Written() {
			c.Status(http.StatusOK)
		}
		return
	}
	if !c.Writer.Written() {
		c.Writer.Header().Del("Content-Disposition")
		c.Writer.Header().Del("Content-Length")
		c.Writer.Header().Del("Content-Type")
		respondInternalError(c, streamErr)
		return
	}
	log.Printf("native download stream failed: %v", streamErr)
	// Let net/http abort the response so a partial download is not accepted.
	panic(http.ErrAbortHandler)
}

func UploadNativeStorageFile(c *gin.Context) {
	dirPath := strings.TrimSpace(c.Query("dir"))
	fileName := strings.TrimSpace(c.Query("fileName"))
	if dirPath == "" || fileName == "" {
		respondError(c, msg.T(msg.LostPart))
		return
	}
	// Extend read deadline for large streaming uploads while keeping default 60s on normal JSON routes.
	rc := http.NewResponseController(c.Writer)
	_ = rc.SetReadDeadline(time.Now().Add(30 * time.Minute))

	handleService(c, func() (interface{}, error) {
		written, err := service.UploadNativeStorageFile(c.Request.Context(), dirPath, fileName, c.Request.Body)
		if err != nil {
			return nil, err
		}
		return gin.H{"fileName": fileName, "size": written}, nil
	})
}

// GetAlistHealth handles GET /svr/alist/health
func GetAlistHealth(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	refresh := c.Query("refresh") == "1" || c.Query("refresh") == "true"
	if idStr := c.Query("id"); idStr != "" {
		id, err := parseRequiredID(idStr)
		if err != nil {
			respondError(c, err.Error())
			return
		}
		if refresh {
			handleService(c, func() (interface{}, error) {
				return service.CheckEngineHealth(c.Request.Context(), id)
			})
			return
		}
		respondOK(c, service.GetEngineHealth(id))
		return
	}
	if refresh {
		handleService(c, func() (interface{}, error) {
			return service.CheckAllEnginesHealth(c.Request.Context())
		})
		return
	}
	handleService(c, func() (interface{}, error) {
		return service.GetClientList()
	})
}

// DeleteAlist handles DELETE /svr/alist
func DeleteAlist(c *gin.Context) {
	// The id comes from the query string only. A form-body fallback could
	// never fire: GatewayRequired rejects writes that are not
	// application/json, and PostForm does not parse JSON.
	idStr := c.Query("id")
	if idStr == "" {
		respondError(c, msg.T(msg.LostPart))
		return
	}
	id, err := parseRequiredID(idStr)
	if err != nil {
		respondError(c, err.Error())
		return
	}
	handleServiceVoid(c, func() error {
		return service.RemoveClient(id)
	})
}
