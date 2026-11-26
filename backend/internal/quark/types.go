package quark

import (
	"errors"
	"strings"
	"time"
)

const (
	ProviderQuark = "quark"
	ProviderUC    = "uc"

	DefaultRootFolderID      = "0"
	DefaultTPSLimit          = 3.5
	DefaultUploadConcurrency = 4
	DefaultPartSize          = 10 << 20 // 10 MiB fallback if server omits part_size
	DefaultCacheTTL          = 30 * time.Second
	DefaultDownloadURLTTL    = 5 * time.Minute
	KeepaliveInterval        = 45 * time.Minute

	ossUserAgent = "aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit"
)

var (
	ErrUnauthorized = errors.New("夸克网盘登录状态已失效，请重新扫码或更新 Cookie")
	ErrRateLimited  = errors.New("夸克网盘接口触发限流，请降低并发或稍后重试")
	ErrInvalidName  = errors.New("文件名包含非法字符")
)

// ProviderProfile defines endpoint & header differences between Quark and UC
// (which share the identical clouddrive protocol).
type ProviderProfile struct {
	Name       string
	APIURL     string
	AccountURL string
	QRAuthURL  string
	Referer    string
	Origin     string
	PR         string
	DefaultUA  string
}

var providerProfiles = map[string]ProviderProfile{
	ProviderQuark: {
		Name:       ProviderQuark,
		APIURL:     "https://drive-pc.quark.cn/1/clouddrive",
		AccountURL: "https://pan.quark.cn",
		QRAuthURL:  "https://uop.quark.cn",
		Referer:    "https://pan.quark.cn/",
		Origin:     "https://pan.quark.cn",
		PR:         "ucpro",
		DefaultUA:  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) quark-cloud-drive/2.5.20 Chrome/100.0.4896.160 Electron/18.3.5.4-b478491100 Safari/537.36 Channel/pckk_other_ch",
	},
	ProviderUC: {
		Name:       ProviderUC,
		APIURL:     "https://pc-api.uc.cn/1/clouddrive",
		AccountURL: "https://drive.uc.cn",
		QRAuthURL:  "https://uop.uc.cn",
		Referer:    "https://drive.uc.cn/",
		Origin:     "https://drive.uc.cn",
		PR:         "UCBrowser",
		DefaultUA:  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) uc-cloud-drive/2.5.20 Chrome/100.0.4896.160 Electron/18.3.5.4-b478491100 Safari/537.36 Channel/pckk_other_ch",
	},
}

// ProfileFor returns the protocol profile for provider ("quark" or "uc").
// Unknown or empty values default to Quark.
func ProfileFor(provider string) ProviderProfile {
	key := strings.ToLower(strings.TrimSpace(provider))
	if p, ok := providerProfiles[key]; ok {
		return p
	}
	return providerProfiles[ProviderQuark]
}

// Options configures a Quark/UC protocol client.
type Options struct {
	Provider           string
	Cookie             string
	RootFolderID       string
	UserAgent          string
	ProxyURL           string
	InsecureSkipVerify bool
	TPSLimit           float64
	UploadConcurrency  int
	PartSize           int64
	CacheTTL           time.Duration
	DownloadURLTTL     time.Duration
	OnCookieUpdate     func(newCookie string)

	// Optional endpoint overrides (primarily for unit tests).
	APIURL     string
	AccountURL string
	QRAuthURL  string
}

// Entry represents a normalized file or folder on Quark/UC Cloud Drive.
type Entry struct {
	FID       string
	ParentFID string
	Name      string
	IsDir     bool
	Size      int64
	ModTime   time.Time
	MD5       string
}

// Quota reports cloud drive capacity returned by /member.
type Quota struct {
	Total      int64
	Used       int64
	Free       int64
	MemberType string
}

// AccountInfo reports basic account identity returned by /account/info.
type AccountInfo struct {
	Nickname string
	Avatar   string
}

// UploadResult summarizes a completed upload to Quark/UC.
type UploadResult struct {
	FID   string
	Name  string
	Size  int64
	MD5   string
	SHA1  string
	Rapid bool
}

// --- Wire API models ---

type baseResp struct {
	Status    int    `json:"status"`
	Code      int    `json:"code"`
	Message   string `json:"message"`
	Timestamp int64  `json:"timestamp"`
}

func (r baseResp) isOK() bool {
	if r.Code != 0 {
		return false
	}
	if r.Status != 0 && (r.Status < 200 || r.Status >= 300) {
		return false
	}
	return true
}

type apiFileItem struct {
	FID        string `json:"fid"`
	FileName   string `json:"file_name"`
	PdirFID    string `json:"pdir_fid"`
	Size       int64  `json:"size"`
	FormatType string `json:"format_type"`
	Status     int    `json:"status"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
	LUpdatedAt int64  `json:"l_updated_at"`
	Dir        bool   `json:"dir"`
	File       bool   `json:"file"`
	FileType   int    `json:"file_type"`
	MD5        string `json:"md5"`
}

type sortResp struct {
	baseResp
	Data struct {
		List []apiFileItem `json:"list"`
	} `json:"data"`
	Metadata struct {
		Size  int `json:"_size"`
		Page  int `json:"_page"`
		Count int `json:"_count"`
		Total int `json:"_total"`
	} `json:"metadata"`
}

type mkdirResp struct {
	baseResp
	Data struct {
		FID    string `json:"fid"`
		Finish bool   `json:"finish"`
	} `json:"data"`
}

type taskSubmitResp struct {
	baseResp
	Data struct {
		TaskID string `json:"task_id"`
		Finish bool   `json:"finish"`
	} `json:"data"`
}

type taskStatusResp struct {
	baseResp
	Data struct {
		TaskID  string `json:"task_id"`
		Status  int    `json:"status"` // 0: pending, 1: running, 2: finished, 3: failed
		Message string `json:"message"`
	} `json:"data"`
}

type downloadResp struct {
	baseResp
	Data []struct {
		FID         string `json:"fid"`
		FileName    string `json:"file_name"`
		Size        int64  `json:"size"`
		DownloadURL string `json:"download_url"`
		MD5         string `json:"md5"`
	} `json:"data"`
}

type memberResp struct {
	baseResp
	Data struct {
		MemberType    string `json:"member_type"`
		UseCapacity   int64  `json:"use_capacity"`
		TotalCapacity int64  `json:"total_capacity"`
	} `json:"data"`
}

type accountInfoResp struct {
	Success bool   `json:"success"`
	Code    string `json:"code"`
	Data    struct {
		Nickname  string `json:"nickname"`
		AvatarURI string `json:"avatarUri"`
	} `json:"data"`
}

type upCallback struct {
	CallbackURL  string `json:"callbackUrl"`
	CallbackBody string `json:"callbackBody"`
}

type upPreResp struct {
	baseResp
	Data struct {
		TaskID     string     `json:"task_id"`
		Finish     bool       `json:"finish"`
		UploadID   string     `json:"upload_id"`
		ObjKey     string     `json:"obj_key"`
		UploadURL  string     `json:"upload_url"`
		FID        string     `json:"fid"`
		Bucket     string     `json:"bucket"`
		Callback   upCallback `json:"callback"`
		FormatType string     `json:"format_type"`
		Size       int64      `json:"size"`
		AuthInfo   string     `json:"auth_info"`
	} `json:"data"`
	Metadata struct {
		PartSize   int64 `json:"part_size"`
		PartThread int   `json:"part_thread"`
	} `json:"metadata"`
}

type upHashResp struct {
	baseResp
	Data struct {
		Finish bool   `json:"finish"`
		FID    string `json:"fid"`
	} `json:"data"`
}

type upAuthResp struct {
	baseResp
	Data struct {
		AuthKey string `json:"auth_key"`
	} `json:"data"`
}

type upFinishResp struct {
	baseResp
	Data struct {
		FID    string `json:"fid"`
		Finish bool   `json:"finish"`
	} `json:"data"`
}
