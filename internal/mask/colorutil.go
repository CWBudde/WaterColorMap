package mask

// abs returns the absolute value of x.
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// max3 returns the maximum of three uint8 values.
func max3(a, b, c uint8) uint8 {
	if a < b {
		a = b
	}
	if a < c {
		a = c
	}
	return a
}

// min3 returns the minimum of three uint8 values.
func min3(a, b, c uint8) uint8 {
	if a > b {
		a = b
	}
	if a > c {
		a = c
	}
	return a
}

// clampU8 clamps an int value to the uint8 range [0, 255].
func clampU8(x int) uint8 {
	if x < 0 {
		return 0
	}
	if x > 255 {
		return 255
	}
	return uint8(x)
}

// rgbToHSL converts RGB (0–255) to HSL using integer math only.
// Hue is returned in [0..1535] (6 * 256 steps), S and L in [0..255].
func rgbToHSL(r, g, b uint8) (h uint16, s, l uint8) {
	maxv := max3(r, g, b)
	minv := min3(r, g, b)
	delta := int(maxv) - int(minv)

	// Lightness
	sum := int(maxv) + int(minv) // 0..510
	l = uint8(sum / 2)

	// Saturation
	if delta == 0 {
		s = 0
	} else {
		den := 255 - abs(sum-255) // 0..255
		if den > 0 {
			s = uint8((delta*255 + den/2) / den)
		} else {
			s = 0
		}
	}

	// Hue (0..1535)
	if delta == 0 {
		h = 0
		return
	}

	switch maxv {
	case r:
		// sector 0 or 5
		h = uint16((int(g) - int(b)) * 256 / delta)
		if int(g) < int(b) {
			h += 1536
		}
	case g:
		// sector 2
		h = uint16(512 + (int(b)-int(r))*256/delta)
	case b:
		// sector 4
		h = uint16(1024 + (int(r)-int(g))*256/delta)
	}

	h %= 1536
	return
}

// hslToRGB converts HSL to RGB using integer math only.
// H: [0..1535] (6 * 256 steps), S/L: [0..255].
func hslToRGB(h uint16, s, l uint8) (r, g, b uint8) {
	// Achromatic (gray)
	if s == 0 {
		return l, l, l
	}

	// Chroma C = (1 - |2L-1|) * S
	// With L,S in 0..255:
	// t = 255 - |2L - 255|  (this equals 255*(1-|2L-1|))
	// C = t*S/255
	L := int(l)
	S := int(s)

	t := 255 - abs(2*L-255) // 0..255
	C := (t*S + 127) / 255  // 0..255, rounded
	m := L - (C / 2)        // match offset, stays in-range

	// Sector 0..5 and fractional position 0..255 inside sector
	h %= 1536
	sector := int(h >> 8) // 0..5
	f := int(h & 0xFF)    // 0..255

	// X = C * (1 - |(h' mod 2) - 1|)
	// In this 6*256 space:
	// even sectors (0,2,4): X = C * f/256
	// odd  sectors (1,3,5): X = C * (256-f)/256
	var X int
	if (sector & 1) == 0 {
		X = (C*f + 127) / 256
	} else {
		X = (C*(256-f) + 127) / 256
	}

	var rp, gp, bp int
	switch sector {
	case 0:
		rp, gp, bp = C, X, 0
	case 1:
		rp, gp, bp = X, C, 0
	case 2:
		rp, gp, bp = 0, C, X
	case 3:
		rp, gp, bp = 0, X, C
	case 4:
		rp, gp, bp = X, 0, C
	case 5:
		rp, gp, bp = C, 0, X
	}

	r = clampU8(rp + m)
	g = clampU8(gp + m)
	b = clampU8(bp + m)
	return
}
