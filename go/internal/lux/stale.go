package lux

import (
	"encoding/binary"
	"hash/fnv"
)

// DefaultStaleThreshold is the default number of consecutive identical input
// frames (per block) at which a frame is treated as a frozen dongle cache.
// Live input blocks never repeat exactly — frequency, voltages and runtime
// jitter every poll (2.3 h capture, 2026-10-26: zero repeats in blocks 0-39,
// 40-79, 0-126) — so 3 leaves margin for a duplicated delivery.
const DefaultStaleThreshold = 3

// StaleDetector flags frozen frames: the WAN dongle sometimes serves an
// identical cached input frame for long stretches (observed i26 stuck at
// 2520 for 1.5 h). Each input block (start register + count) is tracked
// independently by hashing its register values. Holding blocks are never
// flagged — configuration is legitimately constant.
type StaleDetector struct {
	threshold int
	blocks    map[blockKey]*blockState
}

type blockKey struct {
	start, count uint16
}

type blockState struct {
	hash uint64
	run  int // consecutive frames with this hash, including the first
}

// NewStaleDetector returns a detector that flags the threshold-th and later
// consecutive identical frames of a block. A threshold below 2 disables it.
func NewStaleDetector(threshold int) *StaleDetector {
	return &StaleDetector{threshold: threshold, blocks: make(map[blockKey]*blockState)}
}

// Observe records pkt and reports whether it is stale (a frozen repeat).
func (d *StaleDetector) Observe(pkt *Packet) bool {
	// Frames without register data (e.g. request echoes) neither count as
	// stale nor reset a block's run.
	if d.threshold < 2 || pkt.RegisterType != "input" || len(pkt.Registers) == 0 {
		return false
	}
	h := hashRegisters(pkt)
	key := blockKey{pkt.StartRegister, pkt.RegisterCount}
	st, ok := d.blocks[key]
	if !ok {
		d.blocks[key] = &blockState{hash: h, run: 1}
		return false
	}
	if st.hash == h {
		st.run++
	} else {
		st.hash, st.run = h, 1
	}
	return st.run >= d.threshold
}

// hashRegisters hashes a packet's register values in register order.
func hashRegisters(pkt *Packet) uint64 {
	h := fnv.New64a()
	var b [2]byte
	for r := pkt.StartRegister; r < pkt.StartRegister+pkt.RegisterCount; r++ {
		binary.LittleEndian.PutUint16(b[:], pkt.Registers[r])
		h.Write(b[:])
	}
	return h.Sum64()
}
