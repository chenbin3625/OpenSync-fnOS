import { describe, expect, it } from "vitest";
import {
  analyzeDstEncryption,
  buildQuickCryptOptions,
  buildStorageCryptTarget,
  canCreateCryptFromRemote,
  composeCryptUnderlyingRemote,
  findReusableCryptRemotes,
  isContainerRequiredBackend,
  normalizeRelativeSubDir,
  replaceDstPathWithEncrypted,
  suggestCryptRemoteName,
  validateCryptRemoteName,
  validateQuickCryptForm,
  type QuickCryptFormValues,
} from "../src/lib/quickCrypt";
import type { NativeRemoteItem } from "../src/types";

function makeRemote(
  name: string,
  type: string,
  options: Record<string, string> = {},
): NativeRemoteItem {
  return {
    name,
    type,
    summary: "",
    options,
    configuredSecrets: [],
    usedByJobs: [],
    usedByCrypts: [],
  };
}

describe("quickCrypt helpers", () => {
  it("detects encryptable and already-encrypted remote destinations", () => {
    const remotes: NativeRemoteItem[] = [
      makeRemote("aliyun", "webdav", { url: "https://dav.example.com" }),
      makeRemote("oss", "s3", { endpoint: "https://oss.example.com" }),
      makeRemote("ro-http", "http", { url: "https://files.example.com" }),
      makeRemote("oss-vault", "crypt", { remote: "oss:bucket/secret" }),
    ];

    const res = analyzeDstEncryption(
      [
        "/本地/Photos",
        "/oss", // bucketless S3 root is invalid and must not be offered
        "/oss/bucket/backup",
        "/aliyun/docs",
        "/ro-http/public", // read-only remote must be excluded
        "/oss-vault/2026",
      ],
      remotes,
      0,
    );

    expect(res.encryptable.map((t) => t.syncPath)).toEqual([
      "/oss/bucket/backup",
      "/aliyun/docs",
    ]);
    expect(res.encryptable[0].rcloneRemote).toBe("oss:bucket/backup");
    expect(res.encryptable[1].rcloneRemote).toBe("aliyun:docs");
    expect(res.encrypted).toEqual([
      { syncPath: "/oss-vault/2026", remoteName: "oss-vault" },
    ]);
  });

  it("matches exact, ancestor reusable crypts and flags overlapping child crypts", () => {
    const remotes: NativeRemoteItem[] = [
      makeRemote("s3", "s3"),
      makeRemote("s3-root-crypt", "crypt", { remote: "s3:bucket" }),
      makeRemote("s3-exact-crypt", "crypt", { remote: "s3:bucket/photos" }),
      makeRemote("s3-child-crypt", "crypt", { remote: "s3:bucket/photos/2026" }),
      makeRemote("other-crypt", "crypt", { remote: "webdav:bucket/photos" }),
    ];

    const matched = findReusableCryptRemotes("s3", "bucket/photos", remotes);
    expect(matched.reusable).toEqual([
      {
        cryptName: "s3-exact-crypt",
        underlyingRemote: "s3:bucket/photos",
        encryptedSyncPath: "/s3-exact-crypt",
        exact: true,
      },
      {
        cryptName: "s3-root-crypt",
        underlyingRemote: "s3:bucket",
        encryptedSyncPath: "/s3-root-crypt/photos",
        exact: false,
      },
    ]);
    expect(matched.overlapping).toEqual(["s3-child-crypt"]);
  });

  it("generates valid, collision-free crypt remote names", () => {
    const existing = [
      makeRemote("aliyun", "webdav"),
      makeRemote("aliyun-backup-crypt", "crypt"),
      makeRemote("aliyun-backup-crypt-2", "crypt"),
    ];

    expect(suggestCryptRemoteName("aliyun", "backup", existing)).toBe(
      "aliyun-backup-crypt-3",
    );
    expect(suggestCryptRemoteName("aliyun", "", existing)).toBe("aliyun-crypt");
    expect(
      validateCryptRemoteName(
        suggestCryptRemoteName("aliyun", "2026/我的相册!@#", existing),
        existing,
      ),
    ).toBe("");
  });

  it("validates relative subdirectories and composes underlying remote paths", () => {
    expect(normalizeRelativeSubDir("")).toEqual({ normalized: "", error: "" });
    expect(normalizeRelativeSubDir("/encrypted/2026/")).toEqual({
      normalized: "encrypted/2026",
      error: "",
    });
    expect(normalizeRelativeSubDir("../escape").error).not.toBe("");
    expect(normalizeRelativeSubDir("a\\b").error).not.toBe("");
    expect(normalizeRelativeSubDir("a:b").error).not.toBe("");

    expect(composeCryptUnderlyingRemote("aliyun", "backup", "encrypted")).toBe(
      "aliyun:backup/encrypted",
    );
    expect(composeCryptUnderlyingRemote("aliyun", "", "")).toBe("aliyun:");
  });

  it("validates quick crypt form fields and builds minimal rclone crypt options", () => {
    const existing = [makeRemote("taken", "crypt")];
    const base: QuickCryptFormValues = {
      name: "taken",
      subDir: "bad/../dir",
      password: "secret-password",
      confirmPassword: "wrong-password",
      password2: "",
      filenameEncryption: "standard",
      filenameEncoding: "base32",
      directoryNameEncryption: "true",
      noDataEncryption: false,
    };

    const errs = validateQuickCryptForm(base, existing);
    expect(errs.name).toContain("已存在");
    expect(errs.subDir).toBeTruthy();
    expect(errs.confirmPassword).toContain("不一致");

    const valid: QuickCryptFormValues = {
      ...base,
      name: "aliyun-crypt",
      subDir: "vault",
      confirmPassword: "secret-password",
      password2: "my-salt",
      filenameEncryption: "obfuscate",
      filenameEncoding: "base64",
      directoryNameEncryption: "false",
      noDataEncryption: true,
    };
    expect(validateQuickCryptForm(valid, existing)).toEqual({});

    expect(
      buildQuickCryptOptions({ remoteName: "aliyun", subPath: "backup" }, valid),
    ).toEqual({
      type: "crypt",
      remote: "aliyun:backup/vault",
      password: "secret-password",
      password2: "my-salt",
      filename_encryption: "obfuscate",
      filename_encoding: "base64",
      directory_name_encryption: "false",
      no_data_encryption: "true",
    });

    // Default advanced options are omitted so rclone defaults apply cleanly.
    expect(
      buildQuickCryptOptions(
        { remoteName: "aliyun", subPath: "backup" },
        {
          ...valid,
          subDir: "",
          password2: "",
          filenameEncryption: "standard",
          filenameEncoding: "base32",
          directoryNameEncryption: "true",
          noDataEncryption: false,
        },
      ),
    ).toEqual({
      type: "crypt",
      remote: "aliyun:backup",
      password: "secret-password",
    });
  });

  it("replaces only the targeted destination path while preserving other targets", () => {
    expect(
      replaceDstPathWithEncrypted(
        ["/aliyun/backup", "/oss/bucket/mirror"],
        "/aliyun/backup",
        "/aliyun-backup-crypt",
      ),
    ).toEqual(["/aliyun-backup-crypt", "/oss/bucket/mirror"]);

    // Deduplicates if the encrypted path is already present.
    expect(
      replaceDstPathWithEncrypted(
        ["/aliyun/backup", "/aliyun-backup-crypt"],
        "/aliyun/backup",
        "/aliyun-backup-crypt",
      ),
    ).toEqual(["/aliyun-backup-crypt"]);
  });

  it("allows creating encrypted storage only from genuine writable remote storage cards", () => {
    const remotes: NativeRemoteItem[] = [
      makeRemote("oss", "s3"),
      makeRemote("nas-smb", "smb"),
      makeRemote("od", "onedrive"),
      makeRemote("oss-crypt", "crypt", { remote: "oss:bucket/enc" }),
      makeRemote("mirror", "http", { url: "https://mirrors.example.com" }),
      makeRemote("zip-ro", "archive", { remote: "od:a.zip" }),
      makeRemote("pool", "union", { upstreams: "oss:bucket od:" }),
      makeRemote("combo", "combine", { upstreams: "a=od:" }),
      makeRemote("split", "chunker", { remote: "od:" }),
      makeRemote("local-alias", "local"),
    ];

    expect(canCreateCryptFromRemote(remotes[0], remotes)).toBe(true);
    expect(canCreateCryptFromRemote(remotes[1], remotes)).toBe(true);
    expect(canCreateCryptFromRemote(remotes[2], remotes)).toBe(true);

    for (const blocked of remotes.slice(3)) {
      expect(canCreateCryptFromRemote(blocked, remotes)).toBe(false);
    }

    expect(isContainerRequiredBackend("s3")).toBe(true);
    expect(isContainerRequiredBackend("smb")).toBe(true);
    expect(isContainerRequiredBackend("onedrive")).toBe(false);

    const ossTarget = buildStorageCryptTarget(remotes[0], remotes);
    expect(ossTarget.requiresSubDir).toBe(true);
    expect(ossTarget.overlappingCrypts).toEqual(["oss-crypt"]);
    expect(ossTarget.rcloneRemote).toBe("oss:");

    const odTarget = buildStorageCryptTarget(remotes[2], remotes);
    expect(odTarget.requiresSubDir).toBe(false);
    expect(odTarget.rcloneRemote).toBe("od:");

    const emptySubDirValues: QuickCryptFormValues = {
      name: "oss-vault",
      subDir: "",
      password: "secret-password",
      confirmPassword: "secret-password",
      password2: "",
      filenameEncryption: "standard",
      filenameEncoding: "base32",
      directoryNameEncryption: "true",
      noDataEncryption: false,
    };
    expect(
      validateQuickCryptForm(emptySubDirValues, remotes, { requireSubDir: true }),
    ).toHaveProperty("subDir");
    expect(
      validateQuickCryptForm(
        { ...emptySubDirValues, subDir: "my-bucket/encrypted" },
        remotes,
        { requireSubDir: true },
      ),
    ).toEqual({});

    const withMeta = buildQuickCryptOptions(
      { remoteName: "od", subPath: "" },
      { ...emptySubDirValues, subDir: "vault" },
      { managedByJob: true, managedJobLabel: "相册同步" },
    );
    expect(withMeta.managed_by_job).toBe("true");
    expect(withMeta.managed_job_label).toBe("相册同步");
  });
});
