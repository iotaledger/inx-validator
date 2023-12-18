package validator

import (
	"context"
	"time"

	"github.com/iotaledger/inx-app/pkg/nodebridge"
	iotago "github.com/iotaledger/iota.go/v4"
)

const (
	TimeoutINXReadIsCandidate        = 1 * time.Second
	TimeoutINXReadIsCommitteeMember  = 1 * time.Second
	TimeoutINXReadIsValidatorAccount = 1 * time.Second
	TimeoutINXRequestTips            = 2 * time.Second
	TimeoutINXSubmitBlock            = 2 * time.Second
	TimeoutINXActiveRootBlocks       = 2 * time.Second
	TimeoutINXForceCommitUntil       = 5 * time.Second
	TimeoutINXCommitment             = 1 * time.Second
)

// readIsCandidate returns true if the given account is a candidate.
func readIsCandidate(ctx context.Context, accountID iotago.AccountID, slot iotago.SlotIndex) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutINXReadIsCandidate)
	defer cancel()

	return deps.NodeBridge.ReadIsCandidate(ctx, accountID, slot)
}

// readIsCommitteeMember returns true if the given account is a committee member.
func readIsCommitteeMember(ctx context.Context, accountID iotago.AccountID, slot iotago.SlotIndex) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutINXReadIsCommitteeMember)
	defer cancel()

	return deps.NodeBridge.ReadIsCommitteeMember(ctx, accountID, slot)
}

// readIsValidatorAccount returns true if the given account is a validator account.
func readIsValidatorAccount(ctx context.Context, accountID iotago.AccountID, slot iotago.SlotIndex) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutINXReadIsValidatorAccount)
	defer cancel()

	return deps.NodeBridge.ReadIsValidatorAccount(ctx, accountID, slot)
}

// requestTips requests tips from the node.
func requestTips(ctx context.Context, count uint32) (iotago.BlockIDs, iotago.BlockIDs, iotago.BlockIDs, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutINXRequestTips)
	defer cancel()

	return deps.NodeBridge.RequestTips(ctx, count)
}

// submitBlock submits a block to the node.
func submitBlock(ctx context.Context, block *iotago.Block) (iotago.BlockID, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutINXSubmitBlock)
	defer cancel()

	return deps.NodeBridge.SubmitBlock(ctx, block)
}

// activeRootBlocks returns the active root blocks.
func activeRootBlocks(ctx context.Context) (map[iotago.BlockID]iotago.CommitmentID, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutINXActiveRootBlocks)
	defer cancel()

	return deps.NodeBridge.ActiveRootBlocks(ctx)
}

// forceCommitUntil forces the node to commit until the given slot.
func forceCommitUntil(ctx context.Context, slot iotago.SlotIndex) error {
	ctx, cancel := context.WithTimeout(ctx, TimeoutINXForceCommitUntil)
	defer cancel()

	return deps.NodeBridge.ForceCommitUntil(ctx, slot)
}

// commitment returns the commitment for the given slot.
func commitment(ctx context.Context, slot iotago.SlotIndex) (*nodebridge.Commitment, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutINXCommitment)
	defer cancel()

	return deps.NodeBridge.Commitment(ctx, slot)
}
