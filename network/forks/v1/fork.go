package v0

import "github.com/bloxapp/ssv/network/forks"

// ForkV1 is the genesis version 0 implementation
type ForkV1 struct {
}

// New returns an instance of ForkV0
func New() forks.Fork {
	return &ForkV1{}
}

// SlotTick implementation
func (v1 *ForkV1) SlotTick(slot uint64) {

}
