package handler

import (
	"opensync/internal/msg"
	"opensync/internal/service"

	"github.com/gin-gonic/gin"
)

// GetAlist handles GET /svr/alist
func GetAlist(c *gin.Context) {
	// Check if it's a path select request
	alistIDStr := c.Query("alistId")
	path := c.Query("path")
	if alistIDStr != "" && path != "" {
		alistID, err := parseRequiredID(alistIDStr)
		if err != nil {
			respondError(c, err.Error())
			return
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

// AddAlist handles POST /svr/alist
func AddAlist(c *gin.Context) {
	var req map[string]interface{}
	if !bindJSON(c, &req) {
		return
	}
	if req == nil {
		respondError(c, msg.T(msg.LostPart))
		return
	}
	handleServiceVoid(c, func() error {
		return service.AddClient(req)
	})
}

// UpdateAlist handles PUT /svr/alist
func UpdateAlist(c *gin.Context) {
	var req map[string]interface{}
	if !bindJSON(c, &req) {
		return
	}
	if req == nil {
		respondError(c, msg.T(msg.LostPart))
		return
	}
	handleServiceVoid(c, func() error {
		return service.UpdateClient(req)
	})
}

// TestAlist handles POST /svr/alist/test
func TestAlist(c *gin.Context) {
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
	handleService(c, func() (interface{}, error) {
		return service.TestClientWithHealth(c.Request.Context(), id)
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
