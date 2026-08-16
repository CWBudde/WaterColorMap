//go:build amd64

#include "textflag.h"

// BOXDIV turns eight accumulator dwords in Y0 into eight output bytes at
// (DI)(BX*1), reproducing boxDiv: (uint64(acc+half) * uint64(recip)) >> 32.
//
// Y14 holds half and Y15 the reciprocal, both broadcast. The widening multiply
// only covers even dwords, so the odd ones are shifted down, multiplied, and
// blended back; the high dword of each product is what the shift keeps.
//
// Every lane is at most 255 by construction, so neither pack saturates. Packing
// a register against itself leaves the four results of each 128-bit lane in that
// lane's low four bytes.
#define BOXDIV                     \
	VPADDD    Y14, Y0, Y1;     \
	VPMULUDQ  Y15, Y1, Y2;     \
	VPSRLQ    $32, Y1, Y3;     \
	VPMULUDQ  Y15, Y3, Y3;     \
	VPSRLQ    $32, Y2, Y2;     \
	VPBLENDD  $0xaa, Y3, Y2, Y2; \
	VPACKUSDW Y2, Y2, Y2;      \
	VPACKUSWB Y2, Y2, Y2;      \
	MOVL      X2, AX;          \
	MOVL      AX, (DI)(BX*1);  \
	VEXTRACTI128 $1, Y2, X3;   \
	MOVL      X3, AX;          \
	MOVL      AX, 4(DI)(BX*1)

// func BoxAccumAVX2(acc *uint32, row *byte, n int)
//
// Argument layout (ABI0): acc+0(FP)  row+8(FP)  n+16(FP)
//
// The kernel is a leaf: no calls, no locals, no pointers written to the stack.
TEXT ·BoxAccumAVX2(SB), NOSPLIT|NOFRAME, $0-24
	MOVQ acc+0(FP), DI
	MOVQ row+8(FP), SI
	MOVQ n+16(FP), R9

	// The loop below is a do-while, so an empty row would still write a block.
	TESTQ R9, R9
	JLE   empty

	XORQ BX, BX

accum:
	VPMOVZXBD (SI)(BX*1), Y0
	VPADDD    (DI)(BX*4), Y0, Y0
	VMOVDQU   Y0, (DI)(BX*4)

	ADDQ $8, BX
	CMPQ BX, R9
	JLT  accum

empty:
	VZEROUPPER
	RET

// func BoxColsRowAVX2(out *byte, acc *uint32, add, sub *byte, half, recip uint32, n int)
//
// Argument layout (ABI0). half and recip are 32 bits wide and pack next to each
// other; n is realigned to 8:
//   out+0(FP)  acc+8(FP)  add+16(FP)  sub+24(FP)
//   half+32(FP)  recip+36(FP)  n+40(FP)
//
// Registers:
//   DI  out base   SI  acc base   R8  add base   R10 sub base
//   R9  n          BX  current column          AX  scratch
//
// The kernel is a leaf: no calls, no locals, no pointers written to the stack.
TEXT ·BoxColsRowAVX2(SB), NOSPLIT|NOFRAME, $0-48
	MOVQ out+0(FP), DI
	MOVQ acc+8(FP), SI
	MOVQ add+16(FP), R8
	MOVQ sub+24(FP), R10
	MOVQ n+40(FP), R9

	// Broadcasting straight from the frame would read the argument at a width
	// vet cannot match to a uint32, so both go through a register first.
	MOVL         half+32(FP), AX
	MOVL         AX, X14
	VPBROADCASTD X14, Y14
	MOVL         recip+36(FP), AX
	MOVL         AX, X15
	VPBROADCASTD X15, Y15

	// The loop below is a do-while, so an empty row would still write a block.
	TESTQ R9, R9
	JLE   empty

	XORQ BX, BX

	// The first output row of a pass has no row to slide in: its window was
	// primed by the caller.
	CMPQ R8, $0
	JEQ  divide

slide:
	VMOVDQU   (SI)(BX*4), Y0
	VPMOVZXBD (R8)(BX*1), Y1
	VPADDD    Y1, Y0, Y0
	VPMOVZXBD (R10)(BX*1), Y1
	VPSUBD    Y1, Y0, Y0
	VMOVDQU   Y0, (SI)(BX*4)

	BOXDIV

	ADDQ $8, BX
	CMPQ BX, R9
	JLT  slide

	VZEROUPPER
	RET

divide:
	VMOVDQU (SI)(BX*4), Y0

	BOXDIV

	ADDQ $8, BX
	CMPQ BX, R9
	JLT  divide

empty:
	VZEROUPPER
	RET
