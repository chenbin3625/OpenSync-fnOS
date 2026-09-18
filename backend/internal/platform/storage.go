package platform

import (
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"net/http"
	"opensync/internal/config"
	"opensync/internal/mapper"
	"opensync/internal/service"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Mapping struct {
	Path        string `json:"path"`
	AlistID     int64  `json:"alistId"`
	VirtualPath string `json:"virtualPath"`
	Remark      string `json:"remark"`
}

var storageMu sync.Mutex

func ValidateLocalPath(path string, roots []string) error {
	if !filepath.IsAbs(path) {
		return errors.New("目录必须是绝对路径")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return errors.New("目录不存在或不可访问")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return errors.New("目标不是有效目录")
	}
	for _, root := range roots {
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(realRoot, resolved)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}
	}
	return errors.New("目录不在飞牛授权范围内")
}

func ValidateMapping(mapping Mapping, roots []string) error {
	if err := ValidateLocalPath(mapping.Path, roots); err != nil {
		return err
	}
	if mapping.AlistID <= 0 {
		return errors.New("请选择 OpenList / AList 引擎")
	}
	if !strings.HasPrefix(mapping.VirtualPath, "/") || strings.Contains(mapping.VirtualPath, "\\") {
		return errors.New("请输入引擎绝对路径")
	}
	for _, part := range strings.Split(mapping.VirtualPath, "/") {
		if part == ".." || part == "." {
			return errors.New("引擎路径包含非法片段")
		}
	}
	return nil
}

func mappingFile() string { return filepath.Join(config.DataDir(), "local-mappings.json") }
func readMappings() ([]Mapping, error) {
	var mappings []Mapping
	data, err := os.ReadFile(mappingFile())
	if os.IsNotExist(err) {
		return []Mapping{}, nil
	}
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(data, &mappings)
	return mappings, err
}

func RegisterStorage(r *gin.RouterGroup) {
	r.GET("/storage", func(c *gin.Context) {
		roots, err := AuthorizedPaths(c.Request.Context())
		if err != nil {
			c.JSON(200, gin.H{"code": 200, "msg": "ok", "data": gin.H{"paths": []string{}, "mappings": []Mapping{}, "available": false, "reason": err.Error()}})
			return
		}
		uid := c.GetInt64("uid")
		allowed := []string{}
		for _, root := range roots {
			if CheckACL(c.Request.Context(), uid, root) == nil {
				allowed = append(allowed, root)
			}
		}
		storageMu.Lock()
		mappings, err := readMappings()
		storageMu.Unlock()
		if err != nil {
			c.JSON(500, gin.H{"code": 500, "msg": "存储映射读取失败"})
			return
		}
		visible := []Mapping{}
		for _, m := range mappings {
			if ValidateLocalPath(m.Path, allowed) == nil && CheckACL(c.Request.Context(), uid, m.Path) == nil {
				visible = append(visible, m)
			}
		}
		c.JSON(200, gin.H{"code": 200, "msg": "ok", "data": gin.H{"paths": allowed, "mappings": visible, "available": true}})
	})
	r.PUT("/storage", func(c *gin.Context) {
		var mapping Mapping
		if c.ShouldBindJSON(&mapping) != nil {
			c.JSON(400, gin.H{"code": 400, "msg": "映射参数无效"})
			return
		}
		roots, err := AuthorizedPaths(c.Request.Context())
		if err == nil {
			err = ValidateMapping(mapping, roots)
		}
		if err == nil {
			err = CheckACL(c.Request.Context(), c.GetInt64("uid"), mapping.Path)
		}
		if err != nil {
			c.JSON(403, gin.H{"code": 403, "msg": err.Error()})
			return
		}
		if _, err := mapper.GetAlistByID(mapping.AlistID); err != nil {
			c.JSON(400, gin.H{"code": 400, "msg": "引擎不存在"})
			return
		}
		// Check accessibility, not whether the virtual path points to this exact local directory.
		service.GetChildPath(c.Request.Context(), mapping.AlistID, mapping.VirtualPath)
		storageMu.Lock()
		defer storageMu.Unlock()
		mappings, err := readMappings()
		if err != nil {
			c.JSON(500, gin.H{"code": 500, "msg": "存储映射读取失败"})
			return
		}
		found := false
		for i, m := range mappings {
			if m.Path == mapping.Path {
				mappings[i] = mapping
				found = true
			}
		}
		if !found {
			mappings = append(mappings, mapping)
		}
		if err := writeMappings(mappings); err != nil {
			c.JSON(500, gin.H{"code": 500, "msg": "映射保存失败"})
			return
		}
		c.JSON(200, gin.H{"code": 200, "msg": "ok"})
	})
	r.DELETE("/storage", func(c *gin.Context) {
		storageMu.Lock()
		defer storageMu.Unlock()
		mappings, err := readMappings()
		if err != nil {
			c.JSON(500, gin.H{"code": 500, "msg": "存储映射读取失败"})
			return
		}
		next := []Mapping{}
		for _, m := range mappings {
			if m.Path != c.Query("path") {
				next = append(next, m)
			}
		}
		if err := writeMappings(next); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "msg": "映射删除失败"})
			return
		}
		c.JSON(200, gin.H{"code": 200, "msg": "ok"})
	})
}

func writeMappings(mappings []Mapping) error {
	data, err := json.MarshalIndent(mappings, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(config.DataDir(), ".mappings-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), mappingFile())
}
