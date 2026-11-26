package service

import (
	"context"
	"testing"

	"opensync/internal/native"
)

func TestNativeStorageAuthSessionServiceValidation(t *testing.T) {
	ctx := context.Background()

	if _, err := StartNativeStorageAuth(ctx, NativeStorageAuthStartInput{}); err == nil {
		t.Fatal("expected error when storage type is missing")
	}
	if _, err := GetNativeStorageAuth(""); err == nil {
		t.Fatal("expected error when sessionId is missing")
	}
	if _, err := SubmitNativeStorageAuthCallback(ctx, NativeStorageAuthCallbackInput{SessionID: "s1"}); err == nil {
		t.Fatal("expected error when callbackUrl is missing")
	}
	if _, err := ContinueNativeStorageAuth(ctx, NativeStorageAuthContinueInput{}); err == nil {
		t.Fatal("expected error when sessionId is missing")
	}
	if err := CancelNativeStorageAuth(""); err == nil {
		t.Fatal("expected error when sessionId is missing")
	}

	// Non-existent authSessionId must fail merge cleanly.
	if _, _, err := mergeAuthSessionIntoParams("nonexistent-session", map[string]string{"type": "onedrive"}, nil, nil); err == nil {
		t.Fatal("expected mergeAuthSessionIntoParams to fail for unknown session")
	}

	// Empty authSessionId is a clean no-op.
	in := map[string]string{"type": "onedrive", "region": "cn"}
	got, skip, err := mergeAuthSessionIntoParams("", in, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error for empty authSessionId: %v", err)
	}
	if got["region"] != "cn" || len(skip) != 0 {
		t.Fatalf("unexpected passthrough result: %v / %v", got, skip)
	}

	// Shutdown helper must be idempotent.
	ShutdownNativeAuthSessions()
	native.ShutdownAuthSessions()
}
