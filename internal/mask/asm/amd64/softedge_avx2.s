//go:build amd64

#include "textflag.h"

// Broadcast constants. Every one is loop-invariant and small enough that the
// L1 hit of a memory operand is cheaper than tying up a register for it.
#define VEC8(name, val)      \
	GLOBL name(SB), RODATA|NOPTR, $32 \
	DATA  name+0(SB)/4, val  \
	DATA  name+4(SB)/4, val  \
	DATA  name+8(SB)/4, val  \
	DATA  name+12(SB)/4, val \
	DATA  name+16(SB)/4, val \
	DATA  name+20(SB)/4, val \
	DATA  name+24(SB)/4, val \
	DATA  name+28(SB)/4, val

VEC8(seC255<>, $255)
VEC8(seC1<>, $1)
VEC8(seC127<>, $127)
VEC8(seC256<>, $256)
VEC8(seC512<>, $512)
VEC8(seC1024<>, $1024)
VEC8(seC1536<>, $1536)
VEC8(seC65025<>, $65025)
VEC8(seAlpha<>, $0xff000000)

// seMagic255 divides by 255: for n <= 16581375, n/255 == (n*16843010)>>32.
// TestSoftEdgeDivisionMagic proves the bound exhaustively.
VEC8(seMagic255<>, $16843010)

// Sector selectors for hslToRGB. Bit i of each constant says whether sector i
// takes the chroma C (or the ramp X) for that channel, so a per-lane shift by
// the sector index turns the six-way switch into a multiply by 0 or 1.
//
//	R: C at sectors {0,5}, X at {1,4}
//	G: C at sectors {1,2}, X at {0,3}
//	B: C at sectors {3,4}, X at {2,5}
VEC8(seBitsCR<>, $33)
VEC8(seBitsXR<>, $18)
VEC8(seBitsCG<>, $6)
VEC8(seBitsXG<>, $9)
VEC8(seBitsCB<>, $24)
VEC8(seBitsXB<>, $36)

// func SoftEdgeRowAVX2(dst, src, mask *byte, strength float64, n int)
//
// Argument layout (ABI0):
//   dst+0(FP)  src+8(FP)  mask+16(FP)  strength+24(FP)  n+32(FP)
//
// Registers:
//   DI  dst base   SI  src base   R8  mask base   R9  n
//   BX  current pixel index
//
// Vectors:
//   Y15 strength, broadcast as a double
//   Y0  eight source pixels, then the preserved alpha bytes
//   Y1..Y14 the working set, retagged per stage; see the stage comments
//
// The kernel is a leaf: no calls, no locals, no pointers written to the stack.
// It processes eight pixels per iteration and requires n to be a multiple of
// eight; the caller runs the portable loop over any tail.
TEXT ·SoftEdgeRowAVX2(SB), NOSPLIT|NOFRAME, $0-40
	MOVQ dst+0(FP), DI
	MOVQ src+8(FP), SI
	MOVQ mask+16(FP), R8
	MOVQ n+32(FP), R9

	VBROADCASTSD strength+24(FP), Y15

	XORQ BX, BX

block:
	// Split eight NRGBA pixels into per-channel dwords. Alpha stays where it
	// is, masked off in Y0, so the result can be OR-ed back together at the end
	// without a repack.
	VMOVDQU (SI)(BX*4), Y0
	VPAND   seC255<>(SB), Y0, Y1  // r
	VPSRLD  $8, Y0, Y2
	VPAND   seC255<>(SB), Y2, Y2  // g
	VPSRLD  $16, Y0, Y3
	VPAND   seC255<>(SB), Y3, Y3  // b
	VPAND   seAlpha<>(SB), Y0, Y0

	// rgbToHSL, first half: extremes, chroma and lightness.
	VPMAXUD Y2, Y1, Y4
	VPMAXUD Y3, Y4, Y4    // maxv
	VPMINUD Y2, Y1, Y5
	VPMINUD Y3, Y5, Y5    // minv
	VPSUBD  Y5, Y4, Y6    // delta
	VPADDD  Y5, Y4, Y7    // sum

	// den = 255 - |sum - 255|, the saturation denominator.
	VPSUBD  seC255<>(SB), Y7, Y11
	VPABSD  Y11, Y11
	VMOVDQU seC255<>(SB), Y13
	VPSUBD  Y11, Y13, Y11 // den
	VPSRLD  $1, Y7, Y7    // l = sum/2

	// s = (delta*255 + den/2) / den, zero where delta is zero.
	//
	// The divisor varies per lane, so the quotient goes through single
	// precision. Both operands are exact in float32 and the quotient is at most
	// 256, which puts the rounding error three orders of magnitude below the
	// 1/255 gap that separates a non-integral quotient from an integer, so the
	// truncation matches Go's integer division. Same argument for the hue
	// divide below.
	VPMULLD    seC255<>(SB), Y6, Y8
	VPSRLD     $1, Y11, Y14
	VPADDD     Y14, Y8, Y8
	VPMAXUD    seC1<>(SB), Y11, Y14 // den, floored at 1 so the divide is defined
	VCVTDQ2PS  Y8, Y8
	VCVTDQ2PS  Y14, Y14
	VDIVPS     Y14, Y8, Y8
	VCVTTPS2DQ Y8, Y8
	VPXOR      Y12, Y12, Y12
	VPCMPEQD   Y12, Y6, Y11         // delta == 0
	VPANDN     Y8, Y11, Y8          // s

	// Hue. The scalar switch prefers r over g over b on ties, which is what the
	// masked-out eqG below reproduces.
	VPCMPEQD  Y1, Y4, Y12 // maxv == r
	VPCMPEQD  Y2, Y4, Y13
	VPANDN    Y13, Y12, Y13 // maxv == g, r not already chosen
	VPSUBD    Y2, Y1, Y9    // b sector: r - g
	VPSUBD    Y1, Y3, Y14   // g sector: b - r
	VPBLENDVB Y13, Y14, Y9, Y9
	VPSUBD    Y3, Y2, Y14   // r sector: g - b
	VPBLENDVB Y12, Y14, Y9, Y9

	VPSLLD     $8, Y9, Y9
	VPMAXUD    seC1<>(SB), Y6, Y14 // delta, floored at 1
	VCVTDQ2PS  Y9, Y9
	VCVTDQ2PS  Y14, Y14
	VDIVPS     Y14, Y9, Y9
	VCVTTPS2DQ Y9, Y9              // (numerator*256)/delta, truncated toward zero

	// Sector base: 0 for r, 512 for g, 1024 for b.
	VMOVDQU   seC1024<>(SB), Y14
	VMOVDQU   seC512<>(SB), Y10
	VPBLENDVB Y13, Y10, Y14, Y14
	VPXOR     Y10, Y10, Y10
	VPBLENDVB Y12, Y10, Y14, Y14
	VPADDD    Y14, Y9, Y9

	// The r sector wraps negative hues to the top of the circle.
	VPCMPGTD Y2, Y3, Y14 // b > g
	VPAND    Y12, Y14, Y14
	VPAND    seC1536<>(SB), Y14, Y14
	VPADDD   Y14, Y9, Y9
	VPANDN   Y9, Y11, Y9 // h, zero where delta is zero

	// darkening = 65025 - int(float64(65025 - m*m) * strength).
	//
	// The truncated multiply is done in double-precision lanes because the
	// portable path does it in float64; anything narrower would disagree on
	// products that sit just under an integer.
	VPMOVZXBD    (R8)(BX*1), Y10
	VPMULLD      Y10, Y10, Y10
	VMOVDQU      seC65025<>(SB), Y14
	VPSUBD       Y10, Y14, Y10
	VCVTDQ2PD    X10, Y11
	VEXTRACTI128 $1, Y10, X13
	VCVTDQ2PD    X13, Y13
	VMULPD       Y15, Y11, Y11
	VMULPD       Y15, Y13, Y13
	VCVTTPD2DQY  Y11, X11
	VCVTTPD2DQY  Y13, X13
	VINSERTI128  $1, X13, Y11, Y11
	VMOVDQU      seC65025<>(SB), Y14
	VPSUBD       Y11, Y14, Y10 // darkening

	// lNew = (l * darkening) / 65025, as (n/255)/255. The first divide needs
	// the widening multiply; the second fits the (x + 1 + x>>8)>>8 identity,
	// which holds for every x below 65535.
	VPMULLD  Y7, Y10, Y10
	VMOVDQU  seMagic255<>(SB), Y14
	VPMULUDQ Y14, Y10, Y13
	VPSRLQ   $32, Y10, Y11
	VPMULUDQ Y14, Y11, Y11
	VPSRLQ   $32, Y13, Y13
	VPBLENDD $0xaa, Y11, Y13, Y10
	VPSRLD   $8, Y10, Y13
	VPADDD   Y13, Y10, Y10
	VPADDD   seC1<>(SB), Y10, Y10
	VPSRLD   $8, Y10, Y10 // L

	// hslToRGB. C = (1 - |2L-1|) * S, in eighths of a level.
	VPADDD  Y10, Y10, Y13
	VPSUBD  seC255<>(SB), Y13, Y13
	VPABSD  Y13, Y13
	VMOVDQU seC255<>(SB), Y14
	VPSUBD  Y13, Y14, Y13 // t
	VPMULLD Y8, Y13, Y13
	VPADDD  seC127<>(SB), Y13, Y13
	VPSRLD  $8, Y13, Y14
	VPADDD  Y14, Y13, Y13
	VPADDD  seC1<>(SB), Y13, Y13
	VPSRLD  $8, Y13, Y13 // C

	VPSRLD $1, Y13, Y14
	VPSUBD Y14, Y10, Y11 // m = L - C/2, may be negative

	VPSRLD $8, Y9, Y12          // sector
	VPAND  seC255<>(SB), Y9, Y9 // position within the sector

	// X = C * (1 - |(h mod 2) - 1|): a rising ramp in even sectors, falling in odd.
	VPMULLD Y9, Y13, Y14
	VPADDD  seC127<>(SB), Y14, Y14
	VPSRLD  $8, Y14, Y14 // rising
	VMOVDQU seC256<>(SB), Y1
	VPSUBD  Y9, Y1, Y2
	VPMULLD Y13, Y2, Y2
	VPADDD  seC127<>(SB), Y2, Y2
	VPSRLD  $8, Y2, Y2 // falling

	VPAND     seC1<>(SB), Y12, Y3
	VPXOR     Y4, Y4, Y4
	VPCMPEQD  Y4, Y3, Y3
	VPBLENDVB Y3, Y14, Y2, Y2 // X

	// Per channel: pick C, X or nothing by sector, add the match offset, clamp.
	VMOVDQU seBitsCR<>(SB), Y9
	VPSRLVD Y12, Y9, Y9
	VPAND   seC1<>(SB), Y9, Y9
	VMOVDQU seBitsXR<>(SB), Y14
	VPSRLVD Y12, Y14, Y14
	VPAND   seC1<>(SB), Y14, Y14
	VPMULLD Y13, Y9, Y9
	VPMULLD Y2, Y14, Y14
	VPADDD  Y14, Y9, Y1
	VPADDD  Y11, Y1, Y1
	VPMAXSD Y4, Y1, Y1
	VPMINSD seC255<>(SB), Y1, Y1

	VMOVDQU seBitsCG<>(SB), Y9
	VPSRLVD Y12, Y9, Y9
	VPAND   seC1<>(SB), Y9, Y9
	VMOVDQU seBitsXG<>(SB), Y14
	VPSRLVD Y12, Y14, Y14
	VPAND   seC1<>(SB), Y14, Y14
	VPMULLD Y13, Y9, Y9
	VPMULLD Y2, Y14, Y14
	VPADDD  Y14, Y9, Y5
	VPADDD  Y11, Y5, Y5
	VPMAXSD Y4, Y5, Y5
	VPMINSD seC255<>(SB), Y5, Y5

	VMOVDQU seBitsCB<>(SB), Y9
	VPSRLVD Y12, Y9, Y9
	VPAND   seC1<>(SB), Y9, Y9
	VMOVDQU seBitsXB<>(SB), Y14
	VPSRLVD Y12, Y14, Y14
	VPAND   seC1<>(SB), Y14, Y14
	VPMULLD Y13, Y9, Y9
	VPMULLD Y2, Y14, Y14
	VPADDD  Y14, Y9, Y6
	VPADDD  Y11, Y6, Y6
	VPMAXSD Y4, Y6, Y6
	VPMINSD seC255<>(SB), Y6, Y6

	// A zero saturation is achromatic: the scalar path returns (L, L, L)
	// without consulting the hue at all.
	VPCMPEQD  Y4, Y8, Y8
	VPBLENDVB Y8, Y10, Y1, Y1
	VPBLENDVB Y8, Y10, Y5, Y5
	VPBLENDVB Y8, Y10, Y6, Y6

	// Reassemble over the untouched alpha.
	VPSLLD  $8, Y5, Y5
	VPSLLD  $16, Y6, Y6
	VPOR    Y5, Y1, Y1
	VPOR    Y6, Y1, Y1
	VPOR    Y0, Y1, Y1
	VMOVDQU Y1, (DI)(BX*4)

	ADDQ $8, BX
	CMPQ BX, R9
	JLT  block

	VZEROUPPER
	RET
