//go:build amd64

#include "textflag.h"

// func ConvColsRowAVX2(dst, src *byte, aboveOff, belowOff *int32, taps *uint32, radius, w int)
//
// Argument layout (ABI0, all 8 bytes wide):
//   dst+0(FP)  src+8(FP)  aboveOff+16(FP)  belowOff+24(FP)  taps+32(FP)
//   radius+40(FP)  w+48(FP)
//
// Registers:
//   DI  dst base            SI  src base
//   R8  aboveOff base       R9  belowOff base
//   R10 taps base           R11 radius
//   R12 w                   R13 w-8, the offset of the final full block
//   BX  current column offset within the row
//   CX  tap index           AX, DX scratch
//
// Vectors:
//   Y0  accumulator, eight columns as dwords
//   Y1  broadcast tap       Y2, Y3  widened source pixels
//   Y15 broadcast rounding constant 1<<15
//
// The kernel is a leaf: no calls, no locals, no pointers written to the stack.
TEXT ·ConvColsRowAVX2(SB), NOSPLIT|NOFRAME, $0-56
	MOVQ dst+0(FP), DI
	MOVQ src+8(FP), SI
	MOVQ aboveOff+16(FP), R8
	MOVQ belowOff+24(FP), R9
	MOVQ taps+32(FP), R10
	MOVQ radius+40(FP), R11
	MOVQ w+48(FP), R12

	// R13 = offset of the last block that fits fully in the row.
	MOVQ R12, R13
	SUBQ $8, R13

	// Y15 = 1<<15 broadcast, the round-to-nearest bias.
	MOVL $32768, AX
	MOVL AX, X15
	VPBROADCASTD X15, Y15

	XORQ BX, BX

block:
	// Centre tap: acc = src[aboveOff[0] + x] * taps[0].
	MOVL         (R8), AX
	LEAQ         (SI)(AX*1), DX
	VPMOVZXBD    (DX)(BX*1), Y0
	VPBROADCASTD (R10), Y1
	VPMULLD      Y1, Y0, Y0

	// Symmetric taps: acc += (above + below) * taps[k], for k in [1, radius].
	MOVQ $1, CX
	CMPQ R11, $1
	JLT  reduce

taps:
	MOVL      (R8)(CX*4), AX
	LEAQ      (SI)(AX*1), DX
	VPMOVZXBD (DX)(BX*1), Y2

	MOVL      (R9)(CX*4), AX
	LEAQ      (SI)(AX*1), DX
	VPMOVZXBD (DX)(BX*1), Y3

	VPADDD       Y3, Y2, Y2
	VPBROADCASTD (R10)(CX*4), Y1
	VPMULLD      Y1, Y2, Y2
	VPADDD       Y2, Y0, Y0

	INCQ CX
	CMPQ CX, R11
	JLE  taps

reduce:
	// dst[x] = (acc + 1<<15) >> 16, narrowed back to bytes.
	VPADDD Y15, Y0, Y0
	VPSRLD $16, Y0, Y0

	// Every lane is now <= 255, so neither pack saturates. Packing a register
	// against itself leaves the four results of each 128-bit lane in that
	// lane's low four bytes.
	VPACKUSDW Y0, Y0, Y0
	VPACKUSWB Y0, Y0, Y0

	MOVL X0, AX
	MOVL AX, (DI)(BX*1)
	VEXTRACTI128 $1, Y0, X2
	MOVL         X2, AX
	MOVL         AX, 4(DI)(BX*1)

	// Advance a block. Once past the last full block, repeat the final block to
	// cover a ragged tail; that lands BX exactly on w and ends the loop.
	ADDQ $8, BX
	CMPQ BX, R13
	JLE  block
	CMPQ BX, R12
	JGE  done
	MOVQ R13, BX
	JMP  block

done:
	VZEROUPPER
	RET
