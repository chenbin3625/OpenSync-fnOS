package service

import (
	"context"
	"maps"
	"strings"

	"opensync/internal/native"
)

type NativeStorageAuthStartInput struct {
	Type         string            `json:"type"`
	Name         string            `json:"name,omitempty"`
	Editing      bool              `json:"editing,omitempty"`
	Options      map[string]string `json:"options,omitempty"`
	ClearSecrets []string          `json:"clearSecrets,omitempty"`
}

type NativeStorageAuthCallbackInput struct {
	SessionID   string `json:"sessionId"`
	CallbackURL string `json:"callbackUrl"`
}

type NativeStorageAuthContinueInput struct {
	SessionID string `json:"sessionId"`
	State     string `json:"state"`
	Answer    string `json:"answer"`
}

// StartNativeStorageAuth starts a GUI-guided rclone authorization flow for
// OAuth/interactive cloud drive backends. When editing an existing remote,
// omitted secret fields (such as custom client_secret or password) are merged
// from the stored configuration so users do not need to re-type them.
func StartNativeStorageAuth(ctx context.Context, in NativeStorageAuthStartInput) (*native.AuthStepSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	typ := strings.TrimSpace(in.Type)
	if typ == "" && in.Options != nil {
		typ = strings.TrimSpace(in.Options["type"])
	}
	if typ == "" {
		return nil, publicError("请选择存储类型")
	}

	opts := maps.Clone(in.Options)
	if opts == nil {
		opts = map[string]string{}
	}
	opts["type"] = typ

	var skipObscure map[string]bool
	if in.Editing && strings.TrimSpace(in.Name) != "" {
		_, existing, err := loadExistingNativeRow()
		if err != nil {
			return nil, err
		}
		if canonicalName, ok := existing.FindKey(strings.TrimSpace(in.Name)); ok {
			merged, skipped, mergeErr := mergeEditRemoteParams(existing[canonicalName], opts, in.ClearSecrets)
			if mergeErr != nil {
				return nil, mergeErr
			}
			opts = merged
			skipObscure = skipped
		}
	}

	snap, err := native.StartAuthSession(ctx, typ, opts, skipObscure, globalNativeProxyURL())
	if err != nil {
		return nil, publicError(err.Error())
	}
	return snap, nil
}

// GetNativeStorageAuth returns the current status of a guided auth session.
func GetNativeStorageAuth(sessionID string) (*native.AuthStepSnapshot, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, publicError("缺少授权会话标识")
	}
	snap, err := native.GetAuthSessionStatus(sessionID)
	if err != nil {
		return nil, publicError(err.Error())
	}
	return snap, nil
}

// SubmitNativeStorageAuthCallback forwards a browser OAuth redirect URL (or
// query string) to the active rclone loopback listener.
func SubmitNativeStorageAuthCallback(ctx context.Context, in NativeStorageAuthCallbackInput) (*native.AuthStepSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID := strings.TrimSpace(in.SessionID)
	if sessionID == "" {
		return nil, publicError("缺少授权会话标识")
	}
	if strings.TrimSpace(in.CallbackURL) == "" {
		return nil, publicError("请粘贴授权完成后浏览器地址栏中的回调网址")
	}
	snap, err := native.SubmitAuthCallback(ctx, sessionID, in.CallbackURL)
	if err != nil {
		return nil, publicError(err.Error())
	}
	return snap, nil
}

// ContinueNativeStorageAuth submits an answer to the current interactive
// question (drive picker, Team Drive, Jottacloud login token, 2FA code, etc.).
func ContinueNativeStorageAuth(ctx context.Context, in NativeStorageAuthContinueInput) (*native.AuthStepSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID := strings.TrimSpace(in.SessionID)
	if sessionID == "" {
		return nil, publicError("缺少授权会话标识")
	}
	snap, err := native.ContinueAuthSession(ctx, sessionID, strings.TrimSpace(in.State), in.Answer)
	if err != nil {
		return nil, publicError(err.Error())
	}
	return snap, nil
}

// CancelNativeStorageAuth cancels and cleans up a guided auth session.
func CancelNativeStorageAuth(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return publicError("缺少授权会话标识")
	}
	native.CancelAuthSession(sessionID)
	return nil
}

// ShutdownNativeAuthSessions terminates any running auth subprocesses on exit.
func ShutdownNativeAuthSessions() {
	native.ShutdownAuthSessions()
}

// mergeAuthSessionIntoParams overlays credentials and discovered parameters
// from a completed server-side auth session onto incoming form parameters.
// User-entered non-empty values take precedence over auth session defaults,
// while already-obscured fields copied from the auth session are recorded in
// skipObscure so PrepareWithSkipObscure never double-obscures them.
func mergeAuthSessionIntoParams(authSessionID string, incoming map[string]string, clearSecrets []string, skipObscure map[string]bool) (map[string]string, map[string]bool, error) {
	authSessionID = strings.TrimSpace(authSessionID)
	if authSessionID == "" {
		return incoming, skipObscure, nil
	}
	typ := strings.TrimSpace(incoming["type"])
	completed, err := native.LookupCompletedAuthSession(authSessionID, typ)
	if err != nil {
		return nil, nil, publicError(err.Error())
	}
	out := maps.Clone(incoming)
	if out == nil {
		out = map[string]string{}
	}
	if out["type"] == "" {
		out["type"] = completed.Type
	}
	if skipObscure == nil {
		skipObscure = map[string]bool{}
	} else {
		skipObscure = maps.Clone(skipObscure)
	}
	clearSet := map[string]bool{}
	for _, k := range clearSecrets {
		k = strings.TrimSpace(k)
		if k != "" {
			clearSet[k] = true
		}
	}
	for k, v := range completed.Params {
		if k == "type" || v == "" {
			continue
		}
		if clearSet[k] {
			continue
		}
		// Secrets acquired by the fresh auth session should replace omitted
		// form values AND take precedence over older stored tokens when editing.
		if native.IsSecretField(k) {
			if out[k] == "" || k == "token" || k == "trust_token" || k == "cookies" || k == "cookie" || k == "auth_token" {
				out[k] = v
				if completed.ObscuredKeys[k] {
					skipObscure[k] = true
				}
			}
			continue
		}
		if out[k] == "" {
			out[k] = v
		}
	}
	return out, skipObscure, nil
}
