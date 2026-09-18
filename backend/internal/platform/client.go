package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

var apiClient = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/trim_open_gateway_apiscope.socket")
}}}

func call(ctx context.Context, method string, data any, result any) error {
	token := os.Getenv("TRIM_API_TOKEN")
	if token == "" {
		return errors.New("当前环境未提供飞牛开放 API，请从飞牛桌面运行应用")
	}
	body, err := json.Marshal(map[string]any{"reqId": uuid.NewString(), "req": method, "appName": "opensync", "data": data})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://localhost/api/v1/trimapp", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := apiClient.Do(req)
	if err != nil {
		return errors.New("无法访问飞牛开放 API")
	}
	defer response.Body.Close()
	var envelope struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope); err != nil {
		return errors.New("飞牛 API 返回异常")
	}
	if response.StatusCode != 200 || envelope.Code != 0 {
		return fmt.Errorf("飞牛 API: %s", envelope.Msg)
	}
	return json.Unmarshal(envelope.Data, result)
}

func AuthorizedPaths(ctx context.Context) ([]string, error) {
	var result struct {
		Paths []string `json:"paths"`
	}
	err := call(ctx, "trim.file.getSharedAccessibleFolders", map[string]any{}, &result)
	return result.Paths, err
}

func CheckACL(ctx context.Context, uid int64, path string) error {
	var result []struct {
		Readable bool `json:"readable"`
	}
	if err := call(ctx, "trim.file.checkUserACL", map[string]any{"uid": uid, "path": path}, &result); err != nil {
		return err
	}
	if len(result) == 0 || !result[0].Readable {
		return errors.New("当前用户没有此目录的读取权限")
	}
	return nil
}
