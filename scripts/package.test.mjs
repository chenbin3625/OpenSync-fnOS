import { test } from "node:test";
import assert from "node:assert/strict";
import { existsSync, readFileSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { inflateSync } from "node:zlib";
const root = fileURLToPath(new URL("../", import.meta.url));

function readPngMetadata(path) {
  const buffer = readFileSync(path);
  assert.ok(
    buffer.subarray(0, 8).equals(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10])),
    `${path} must be a PNG file`,
  );

  let offset = 8;
  let width = 0;
  let height = 0;
  let bitDepth = 0;
  let colorType = 0;
  const idat = [];
  while (offset < buffer.length) {
    const length = buffer.readUInt32BE(offset);
    offset += 4;
    const type = buffer.subarray(offset, offset + 4).toString("ascii");
    offset += 4;
    const data = buffer.subarray(offset, offset + length);
    offset += length + 4;

    if (type === "IHDR") {
      width = data.readUInt32BE(0);
      height = data.readUInt32BE(4);
      bitDepth = data[8];
      colorType = data[9];
    } else if (type === "IDAT") {
      idat.push(data);
    } else if (type === "IEND") {
      break;
    }
  }

  assert.equal(bitDepth, 8, `${path} must use 8-bit color channels`);
  assert.equal(colorType, 6, `${path} must use RGBA color`);

  const bytesPerPixel = 4;
  const stride = width * bytesPerPixel;
  const raw = inflateSync(Buffer.concat(idat));
  const pixels = Buffer.alloc(height * stride);
  let rawOffset = 0;

  for (let y = 0; y < height; y += 1) {
    const filter = raw[rawOffset];
    rawOffset += 1;
    const row = raw.subarray(rawOffset, rawOffset + stride);
    rawOffset += stride;
    const previous = y === 0 ? null : pixels.subarray((y - 1) * stride, y * stride);
    const output = pixels.subarray(y * stride, (y + 1) * stride);

    for (let x = 0; x < stride; x += 1) {
      const left = x >= bytesPerPixel ? output[x - bytesPerPixel] : 0;
      const up = previous ? previous[x] : 0;
      const upLeft = previous && x >= bytesPerPixel ? previous[x - bytesPerPixel] : 0;
      let predictor = 0;

      if (filter === 1) {
        predictor = left;
      } else if (filter === 2) {
        predictor = up;
      } else if (filter === 3) {
        predictor = Math.floor((left + up) / 2);
      } else if (filter === 4) {
        const p = left + up - upLeft;
        const pa = Math.abs(p - left);
        const pb = Math.abs(p - up);
        const pc = Math.abs(p - upLeft);
        predictor = pa <= pb && pa <= pc ? left : pb <= pc ? up : upLeft;
      } else {
        assert.equal(filter, 0, `${path} uses unsupported PNG filter ${filter}`);
      }

      output[x] = (row[x] + predictor) & 255;
    }
  }

  let minX = width;
  let minY = height;
  let maxX = -1;
  let maxY = -1;
  for (let y = 0; y < height; y += 1) {
    for (let x = 0; x < width; x += 1) {
      if (pixels[y * stride + x * bytesPerPixel + 3] > 8) {
        minX = Math.min(minX, x);
        minY = Math.min(minY, y);
        maxX = Math.max(maxX, x);
        maxY = Math.max(maxY, y);
      }
    }
  }

  return { width, height, bounds: { minX, minY, maxX, maxY } };
}
test("package declares rootless gateway and minimum API scopes", () => {
  assert.ok(existsSync(root + "fnos/manifest"), "native manifest must exist");
  const manifest = readFileSync(root + "fnos/manifest", "utf8");
  assert.match(manifest, /^platform=x86$/m);
  assert.match(manifest, /^install_type=$/m);
  assert.match(manifest, /^micro_app=true$/m);
  assert.match(manifest, /^os_min_version=1.2.0401$/m);
  assert.doesNotMatch(manifest, /^service_port=/m);
  assert.match(manifest, /^checkport=false$/m);
  assert.match(manifest, /^disable_authorization_path=true$/m);
  assert.equal(
    JSON.parse(readFileSync(root + "fnos/config/privilege")).defaults["run-as"],
    "package",
  );
  // 应用只调用统一网关注入的身份头，不再访问共享目录或文件 ACL，
  // 因此不申请任何开放 API 权限。
  assert.deepEqual(
    JSON.parse(readFileSync(root + "fnos/config/resource"))["api-scope"],
    [],
  );
  const entry = JSON.parse(readFileSync(root + "fnos/app/ui/config"))[".url"][
    "opensync.main"
  ];
  assert.equal(entry.gatewayPrefix, "/app/opensync");
  assert.equal(entry.gatewaySocket, "app.sock");
  assert.equal(entry.allUsers, false);
  assert.equal(entry.type, "iframe");
  assert.equal(entry.control.accessPerm, "readonly");
});

test("lifecycle scripts are executable in the package template", () => {
  const scripts = [
    "install_init",
    "install_callback",
    "main",
    "upgrade_init",
    "upgrade_callback",
    "uninstall_init",
    "uninstall_callback",
    "config_init",
    "config_callback",
  ];
  for (const name of scripts) {
    const mode = statSync(root + `fnos/cmd/${name}`).mode;
    assert.ok(mode & 0o111, `fnos/cmd/${name} must be executable`);
  }
});

test("package icons use fnOS dimensions and matching visual bounds", () => {
  const smallIcon = root + "fnos/ICON.PNG";
  const smallUiIcon = root + "fnos/app/ui/images/icon_64.png";
  const largeIcon = root + "fnos/ICON_256.PNG";
  const largeUiIcon = root + "fnos/app/ui/images/icon_256.png";

  assert.deepEqual(readFileSync(smallIcon), readFileSync(smallUiIcon));
  assert.deepEqual(readFileSync(largeIcon), readFileSync(largeUiIcon));

  const small = readPngMetadata(smallIcon);
  const large = readPngMetadata(largeIcon);

  assert.deepEqual(
    { width: small.width, height: small.height, bounds: small.bounds },
    {
      width: 64,
      height: 64,
      bounds: { minX: 2, minY: 2, maxX: 61, maxY: 61 },
    },
  );
  assert.deepEqual(
    { width: large.width, height: large.height, bounds: large.bounds },
    {
      width: 256,
      height: 256,
      bounds: { minX: 8, minY: 8, maxX: 247, maxY: 247 },
    },
  );
});
