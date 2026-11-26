package quark

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Pure-Go ISO/IEC 18004 QR Code encoder (Byte mode, ECC Level M, Versions 1..15)
// used to render scannable Quark login QR SVGs with zero external dependencies.

type qrVersionSpec struct {
	version       int
	totalCodeword int
	ecPerBlock    int
	group1Blocks  int
	group1DataCW  int
	group2Blocks  int
	group2DataCW  int
	alignment     []int
}

// ISO/IEC 18004 Table 9 (Error Correction Level M) & Annex E alignment coordinates.
var qrVersionsM = []qrVersionSpec{
	{1, 26, 10, 1, 16, 0, 0, nil},
	{2, 44, 16, 1, 28, 0, 0, []int{6, 18}},
	{3, 70, 26, 1, 44, 0, 0, []int{6, 22}},
	{4, 100, 18, 2, 32, 0, 0, []int{6, 26}},
	{5, 134, 24, 2, 43, 0, 0, []int{6, 30}},
	{6, 172, 16, 4, 27, 0, 0, []int{6, 34}},
	{7, 196, 18, 4, 31, 0, 0, []int{6, 22, 38}},
	{8, 242, 22, 2, 38, 2, 39, []int{6, 24, 42}},
	{9, 292, 22, 3, 36, 2, 37, []int{6, 26, 46}},
	{10, 346, 26, 4, 43, 1, 44, []int{6, 28, 50}},
	{11, 404, 30, 1, 50, 4, 51, []int{6, 30, 54}},
	{12, 466, 22, 6, 36, 2, 37, []int{6, 32, 58}},
	{13, 532, 22, 8, 37, 1, 38, []int{6, 34, 62}},
	{14, 581, 24, 4, 40, 5, 41, []int{6, 26, 46, 66}},
	{15, 655, 24, 5, 41, 5, 42, []int{6, 26, 48, 70}},
}

// Precomputed GF(256) exp/log tables for primitive polynomial x^8 + x^4 + x^3 + x^2 + 1 (0x11d).
var (
	gfExp [512]byte
	gfLog [256]byte
)

func init() {
	x := 1
	for i := 0; i < 255; i++ {
		gfExp[i] = byte(x)
		gfLog[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11d
		}
	}
	for i := 255; i < 512; i++ {
		gfExp[i] = gfExp[i-255]
	}
}

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])+int(gfLog[b])]
}

func rsGeneratorPoly(degree int) []byte {
	poly := []byte{1}
	for i := 0; i < degree; i++ {
		next := make([]byte, len(poly)+1)
		factor := gfExp[i]
		for j, coef := range poly {
			next[j] ^= coef
			next[j+1] ^= gfMul(coef, factor)
		}
		poly = next
	}
	return poly
}

func rsRemainder(data []byte, ecLen int) []byte {
	gen := rsGeneratorPoly(ecLen)
	rem := make([]byte, ecLen)
	for _, b := range data {
		factor := b ^ rem[0]
		copy(rem, rem[1:])
		rem[ecLen-1] = 0
		if factor != 0 {
			for i := 0; i < ecLen; i++ {
				rem[i] ^= gfMul(gen[i+1], factor)
			}
		}
	}
	return rem
}

// EncodeQRMatrix encodes data into a square boolean QR module matrix (true = dark).
func EncodeQRMatrix(content string) ([][]bool, error) {
	payload := []byte(content)
	if len(payload) == 0 {
		return nil, errors.New("二维码内容为空")
	}

	var spec *qrVersionSpec
	for i := range qrVersionsM {
		v := &qrVersionsM[i]
		dataCW := v.group1Blocks*v.group1DataCW + v.group2Blocks*v.group2DataCW
		countBits := 8
		if v.version >= 10 {
			countBits = 16
		}
		neededBits := 4 + countBits + len(payload)*8
		if neededBits <= dataCW*8 {
			spec = v
			break
		}
	}
	if spec == nil {
		return nil, errors.New("二维码内容超过最大编码长度")
	}

	codewords := buildQRCodewords(payload, spec)
	size := spec.version*4 + 17
	base := make([][]bool, size)
	reserved := make([][]bool, size)
	for y := 0; y < size; y++ {
		base[y] = make([]bool, size)
		reserved[y] = make([]bool, size)
	}

	placeFunctionPatterns(base, reserved, spec)
	placeDataBits(base, reserved, codewords)

	// Evaluate all 8 mask patterns and choose the lowest penalty score.
	bestMask := 0
	bestScore := int(^uint(0) >> 1)
	var bestGrid [][]bool
	for mask := 0; mask < 8; mask++ {
		grid := cloneGrid(base)
		applyMask(grid, reserved, mask)
		placeFormatInfo(grid, mask)
		if spec.version >= 7 {
			placeVersionInfo(grid, spec.version)
		}
		score := evaluatePenalty(grid)
		if score < bestScore {
			bestScore = score
			bestMask = mask
			bestGrid = grid
		}
	}
	_ = bestMask
	return bestGrid, nil
}

// EncodeQRCodeSVGDataURL encodes content into a base64 SVG data URL ready for <img>.
func EncodeQRCodeSVGDataURL(content string) (string, error) {
	grid, err := EncodeQRMatrix(content)
	if err != nil {
		return "", err
	}
	svg := renderQRMatrixSVG(grid, 4)
	return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(svg)), nil
}

func renderQRMatrixSVG(grid [][]bool, quietZone int) string {
	n := len(grid)
	dim := n + quietZone*2
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`, dim, dim)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#ffffff"/>`, dim, dim)
	b.WriteString(`<path fill="#111827" d="`)
	for y := 0; y < n; y++ {
		x := 0
		for x < n {
			if !grid[y][x] {
				x++
				continue
			}
			run := 1
			for x+run < n && grid[y][x+run] {
				run++
			}
			fmt.Fprintf(&b, "M%d %dh%dv1h-%dz", x+quietZone, y+quietZone, run, run)
			x += run
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String()
}

func buildQRCodewords(payload []byte, spec *qrVersionSpec) []byte {
	dataCW := spec.group1Blocks*spec.group1DataCW + spec.group2Blocks*spec.group2DataCW
	totalBits := dataCW * 8
	bits := make([]byte, 0, totalBits)
	appendBits := func(val, count int) {
		for i := count - 1; i >= 0; i-- {
			bits = append(bits, byte((val>>i)&1))
		}
	}

	// Byte mode indicator (0100) + character count indicator.
	appendBits(0b0100, 4)
	if spec.version < 10 {
		appendBits(len(payload), 8)
	} else {
		appendBits(len(payload), 16)
	}
	for _, b := range payload {
		appendBits(int(b), 8)
	}
	// Terminator (up to 4 zero bits).
	term := totalBits - len(bits)
	if term > 4 {
		term = 4
	}
	appendBits(0, term)
	// Pad to byte boundary.
	for len(bits)%8 != 0 {
		bits = append(bits, 0)
	}
	raw := make([]byte, 0, dataCW)
	for i := 0; i < len(bits); i += 8 {
		var b byte
		for j := 0; j < 8; j++ {
			b = (b << 1) | bits[i+j]
		}
		raw = append(raw, b)
	}
	// Pad codewords alternating 0xEC, 0x11.
	padBytes := [2]byte{0xEC, 0x11}
	for i := 0; len(raw) < dataCW; i++ {
		raw = append(raw, padBytes[i%2])
	}

	// Split into blocks and compute Reed-Solomon EC codewords per block.
	numBlocks := spec.group1Blocks + spec.group2Blocks
	dataBlocks := make([][]byte, 0, numBlocks)
	ecBlocks := make([][]byte, 0, numBlocks)
	offset := 0
	for i := 0; i < spec.group1Blocks; i++ {
		blk := raw[offset : offset+spec.group1DataCW]
		offset += spec.group1DataCW
		dataBlocks = append(dataBlocks, blk)
		ecBlocks = append(ecBlocks, rsRemainder(blk, spec.ecPerBlock))
	}
	for i := 0; i < spec.group2Blocks; i++ {
		blk := raw[offset : offset+spec.group2DataCW]
		offset += spec.group2DataCW
		dataBlocks = append(dataBlocks, blk)
		ecBlocks = append(ecBlocks, rsRemainder(blk, spec.ecPerBlock))
	}

	// Interleave data codewords, then EC codewords.
	out := make([]byte, 0, spec.totalCodeword)
	maxDataLen := spec.group1DataCW
	if spec.group2DataCW > maxDataLen {
		maxDataLen = spec.group2DataCW
	}
	for col := 0; col < maxDataLen; col++ {
		for _, blk := range dataBlocks {
			if col < len(blk) {
				out = append(out, blk[col])
			}
		}
	}
	for col := 0; col < spec.ecPerBlock; col++ {
		for _, blk := range ecBlocks {
			out = append(out, blk[col])
		}
	}
	return out
}

func setModule(grid, reserved [][]bool, x, y int, dark bool) {
	if y < 0 || y >= len(grid) || x < 0 || x >= len(grid) {
		return
	}
	grid[y][x] = dark
	reserved[y][x] = true
}

func placeFinder(grid, reserved [][]bool, topX, topY int) {
	for dy := -1; dy <= 7; dy++ {
		for dx := -1; dx <= 7; dx++ {
			x, y := topX+dx, topY+dy
			if x < 0 || y < 0 || x >= len(grid) || y >= len(grid) {
				continue
			}
			dark := false
			if dx >= 0 && dx <= 6 && dy >= 0 && dy <= 6 {
				if dx == 0 || dx == 6 || dy == 0 || dy == 6 || (dx >= 2 && dx <= 4 && dy >= 2 && dy <= 4) {
					dark = true
				}
			}
			setModule(grid, reserved, x, y, dark)
		}
	}
}

func placeAlignment(grid, reserved [][]bool, cx, cy int) {
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			dark := dx == -2 || dx == 2 || dy == -2 || dy == 2 || (dx == 0 && dy == 0)
			setModule(grid, reserved, cx+dx, cy+dy, dark)
		}
	}
}

func placeFunctionPatterns(grid, reserved [][]bool, spec *qrVersionSpec) {
	n := len(grid)
	// Finder patterns + separators.
	placeFinder(grid, reserved, 0, 0)
	placeFinder(grid, reserved, n-7, 0)
	placeFinder(grid, reserved, 0, n-7)

	// Alignment patterns (skip corners overlapping finder patterns).
	for _, cy := range spec.alignment {
		for _, cx := range spec.alignment {
			if reserved[cy][cx] {
				continue
			}
			placeAlignment(grid, reserved, cx, cy)
		}
	}

	// Timing patterns.
	for i := 8; i < n-8; i++ {
		dark := i%2 == 0
		if !reserved[6][i] {
			setModule(grid, reserved, i, 6, dark)
		}
		if !reserved[i][6] {
			setModule(grid, reserved, 6, i, dark)
		}
	}

	// Dark module (always dark at (8, 4*version+9)).
	setModule(grid, reserved, 8, 4*spec.version+9, true)

	// Reserve format info strips.
	for i := 0; i <= 8; i++ {
		if i != 6 {
			reserved[8][i] = true
			reserved[i][8] = true
		}
	}
	for i := 0; i < 8; i++ {
		reserved[8][n-1-i] = true
	}
	for i := 0; i < 7; i++ {
		reserved[n-1-i][8] = true
	}

	// Reserve version info areas for versions >= 7.
	if spec.version >= 7 {
		for i := 0; i < 6; i++ {
			for j := 0; j < 3; j++ {
				reserved[i][n-11+j] = true
				reserved[n-11+j][i] = true
			}
		}
	}
}

func placeDataBits(grid, reserved [][]bool, codewords []byte) {
	n := len(grid)
	bitIdx := 0
	totalBits := len(codewords) * 8
	upward := true
	for right := n - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5 // skip vertical timing column
		}
		for step := 0; step < n; step++ {
			y := step
			if upward {
				y = n - 1 - step
			}
			for dx := 0; dx < 2; dx++ {
				x := right - dx
				if reserved[y][x] {
					continue
				}
				dark := false
				if bitIdx < totalBits {
					dark = ((codewords[bitIdx/8] >> (7 - bitIdx%8)) & 1) != 0
					bitIdx++
				}
				grid[y][x] = dark
			}
		}
		upward = !upward
	}
}

func maskBit(mask, x, y int) bool {
	switch mask {
	case 0:
		return (x+y)%2 == 0
	case 1:
		return y%2 == 0
	case 2:
		return x%3 == 0
	case 3:
		return (x+y)%3 == 0
	case 4:
		return (y/2+x/3)%2 == 0
	case 5:
		return (x*y)%2+(x*y)%3 == 0
	case 6:
		return ((x*y)%2+(x*y)%3)%2 == 0
	case 7:
		return ((x+y)%2+(x*y)%3)%2 == 0
	default:
		return false
	}
}

func applyMask(grid, reserved [][]bool, mask int) {
	n := len(grid)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if !reserved[y][x] && maskBit(mask, x, y) {
				grid[y][x] = !grid[y][x]
			}
		}
	}
}

// placeFormatInfo writes the 15-bit BCH format string for EC Level M (00) + mask.
func placeFormatInfo(grid [][]bool, mask int) {
	// EC Level M = 00 in ISO/IEC 18004 Table 12.
	data := (0b00 << 3) | (mask & 7)
	rem := data
	for i := 0; i < 10; i++ {
		rem = (rem << 1) ^ ((rem >> 9) * 0x537)
	}
	bits := ((data << 10) | rem) ^ 0x5412

	n := len(grid)
	// Top-left vertical + horizontal copies (bit 0 = LSB, bit 14 = MSB).
	for i := 0; i <= 5; i++ {
		grid[i][8] = ((bits >> i) & 1) != 0
	}
	grid[7][8] = ((bits >> 6) & 1) != 0
	grid[8][8] = ((bits >> 7) & 1) != 0
	grid[8][7] = ((bits >> 8) & 1) != 0
	for i := 9; i < 15; i++ {
		grid[8][14-i] = ((bits >> i) & 1) != 0
	}
	// Second copy: top-right horizontal (bits 0..7) & bottom-left vertical (bits 8..14).
	for i := 0; i < 8; i++ {
		grid[8][n-1-i] = ((bits >> i) & 1) != 0
	}
	for i := 8; i < 15; i++ {
		grid[n-15+i][8] = ((bits >> i) & 1) != 0
	}
}

// placeVersionInfo writes the 18-bit Golay version string for versions >= 7.
func placeVersionInfo(grid [][]bool, version int) {
	rem := version
	for i := 0; i < 12; i++ {
		rem = (rem << 1) ^ ((rem >> 11) * 0x1F25)
	}
	bits := (version << 12) | rem
	n := len(grid)
	for i := 0; i < 18; i++ {
		dark := ((bits >> i) & 1) != 0
		a := n - 11 + i%3
		b := i / 3
		grid[b][a] = dark
		grid[a][b] = dark
	}
}

func cloneGrid(src [][]bool) [][]bool {
	out := make([][]bool, len(src))
	for i := range src {
		out[i] = make([]bool, len(src[i]))
		copy(out[i], src[i])
	}
	return out
}

func evaluatePenalty(grid [][]bool) int {
	n := len(grid)
	score := 0

	// Rule 1: 5+ consecutive same-color modules in row or column.
	for y := 0; y < n; y++ {
		runLen := 1
		for x := 1; x < n; x++ {
			if grid[y][x] == grid[y][x-1] {
				runLen++
			} else {
				if runLen >= 5 {
					score += 3 + (runLen - 5)
				}
				runLen = 1
			}
		}
		if runLen >= 5 {
			score += 3 + (runLen - 5)
		}
	}
	for x := 0; x < n; x++ {
		runLen := 1
		for y := 1; y < n; y++ {
			if grid[y][x] == grid[y-1][x] {
				runLen++
			} else {
				if runLen >= 5 {
					score += 3 + (runLen - 5)
				}
				runLen = 1
			}
		}
		if runLen >= 5 {
			score += 3 + (runLen - 5)
		}
	}

	// Rule 2: 2x2 solid blocks.
	for y := 0; y < n-1; y++ {
		for x := 0; x < n-1; x++ {
			c := grid[y][x]
			if grid[y][x+1] == c && grid[y+1][x] == c && grid[y+1][x+1] == c {
				score += 3
			}
		}
	}

	// Rule 3: finder-like 1:1:3:1:1 patterns with 4 light modules on either side.
	p1 := [11]bool{true, false, true, true, true, false, true, false, false, false, false}
	p2 := [11]bool{false, false, false, false, true, false, true, true, true, false, true}
	for y := 0; y < n; y++ {
		for x := 0; x <= n-11; x++ {
			m1, m2 := true, true
			for k := 0; k < 11; k++ {
				if grid[y][x+k] != p1[k] {
					m1 = false
				}
				if grid[y][x+k] != p2[k] {
					m2 = false
				}
			}
			if m1 || m2 {
				score += 40
			}
		}
	}
	for x := 0; x < n; x++ {
		for y := 0; y <= n-11; y++ {
			m1, m2 := true, true
			for k := 0; k < 11; k++ {
				if grid[y+k][x] != p1[k] {
					m1 = false
				}
				if grid[y+k][x] != p2[k] {
					m2 = false
				}
			}
			if m1 || m2 {
				score += 40
			}
		}
	}

	// Rule 4: dark module ratio deviation from 50%.
	dark := 0
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if grid[y][x] {
				dark++
			}
		}
	}
	total := n * n
	diff := dark*20 - total*10
	if diff < 0 {
		diff = -diff
	}
	score += (diff / total) * 10
	return score
}
