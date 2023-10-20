package validator

import (
	"context"
	"time"

	"github.com/iotaledger/hive.go/ierrors"
	iotago "github.com/iotaledger/iota.go/v4"
	"github.com/iotaledger/iota.go/v4/builder"
)

var ErrBlockTooRecent = ierrors.New("block is too recent compared to latest commitment")

// TODO: rename this method as it's more general than that
func tryIssueValidatorBlock(ctx context.Context) {
	blockIssuingTime := time.Now()
	nextBroadcast := blockIssuingTime.Add(ParamsValidator.CommitteeBroadcastInterval)
	currentAPI := deps.NodeBridge.APIProvider().APIForTime(blockIssuingTime)

	// Use 'defer' because nextBroadcast is updated during function execution, and the value at the end needs to be used.
	defer func() {
		executor.ExecuteAt(validatorAccount.ID(), func() { tryIssueValidatorBlock(ctx) }, nextBroadcast)
	}()

	blockSlot := currentAPI.TimeProvider().SlotFromTime(blockIssuingTime)
	isCommitteeMember, err := deps.NodeBridge.ReadIsCommitteeMember(ctx, validatorAccount.ID(), blockSlot)
	if err != nil {
		Component.LogWarnf("error while checking if account %s is a committee member in slot %d: %s", validatorAccount.ID(), blockSlot, err.Error())

		return
	}

	if !isCommitteeMember {
		if !deps.NodeBridge.NodeStatus().GetIsBootstrapped() {
			Component.LogDebug("not issuing candidate block because node is not bootstrapped yet.")

			return
		}

		if err := issueCandidateBlock(ctx, blockIssuingTime, currentAPI); err != nil {
			Component.LogWarnf("error while trying to issue candidacy announcement: %s", err.Error())

			return
		}

		// update nextBroadcast value here, so that this updated value is used in the `defer`
		// callback to schedule issuing of the next block at a different interval than for committee members
		blockEpoch := currentAPI.TimeProvider().EpochFromSlot(blockSlot)
		nextBroadcast = currentAPI.TimeProvider().SlotStartTime(currentAPI.TimeProvider().EpochStart(blockEpoch + 1))

		return
	}

	// Validator block may ignore the bootstrap flag in order to bootstrap the network and begin acceptance.
	if !ParamsValidator.IgnoreBootstrapped && !deps.NodeBridge.NodeStatus().GetIsBootstrapped() {
		Component.LogDebug("not issuing validator block because node is not bootstrapped yet.")

		return
	}

	issueValidatorBlock(ctx, blockIssuingTime, currentAPI)
}

func issueCandidateBlock(ctx context.Context, blockIssuingTime time.Time, currentAPI iotago.API) error {
	blockSlot := currentAPI.TimeProvider().SlotFromTime(blockIssuingTime)

	isCandidate, err := deps.NodeBridge.ReadIsCandidate(ctx, validatorAccount.ID(), blockSlot)
	if err != nil {
		return ierrors.Wrap(err, "error while checking if account is already a candidate")
	}

	if isCandidate {
		Component.LogDebugf("not issuing candidacy announcement block as account %s is already registered as candidate in epoch %d", validatorAccount.ID(), currentAPI.TimeProvider().EpochFromSlot(blockSlot))

		return nil
	}

	if epochNearingThresholdSlot := currentAPI.TimeProvider().EpochEnd(currentAPI.TimeProvider().EpochFromSlot(blockSlot)) - currentAPI.ProtocolParameters().EpochNearingThreshold(); blockSlot > epochNearingThresholdSlot {
		Component.LogDebugf("not issuing candidacy announcement for account %s as the slot the block would be issued in (%d) is past the Epoch Nearing Threshold (%d)", validatorAccount.ID(), blockSlot, epochNearingThresholdSlot)

		return nil
	}

	strongParents, weakParents, shallowLikeParents, err := deps.NodeBridge.RequestTips(ctx, iotago.BlockMaxParents)

	addressableCommitment, err := getAddressableCommitment(ctx, blockSlot)
	if err != nil {
		return ierrors.Wrap(err, "error getting commitment")
	}

	// create the validation block here using the validation block builder from iota.go
	candidacyBlock, err := builder.NewBasicBlockBuilder(currentAPI).
		IssuingTime(blockIssuingTime).
		SlotCommitmentID(addressableCommitment.MustID()).
		LatestFinalizedSlot(deps.NodeBridge.LatestFinalizedCommitmentID().Slot()).
		StrongParents(strongParents).
		WeakParents(weakParents).
		ShallowLikeParents(shallowLikeParents).
		Payload(&iotago.CandidacyAnnouncement{}).
		CalculateAndSetMaxBurnedMana(addressableCommitment.ReferenceManaCost).
		Sign(validatorAccount.ID(), validatorAccount.PrivateKey()).
		Build()
	if err != nil {
		return ierrors.Wrap(err, "error creating candidacy announcement block")
	}

	blockID, err := deps.NodeBridge.SubmitBlock(ctx, candidacyBlock)
	if err != nil {
		return ierrors.Wrap(err, "error issuing candidacy announcement block")
	}

	Component.LogDebugf("issued candidacy announcement block: %s - commitment %s %d - latest finalized slot %d", blockID, candidacyBlock.SlotCommitmentID, candidacyBlock.SlotCommitmentID.Slot(), candidacyBlock.LatestFinalizedSlot)

	return nil
}

func issueValidatorBlock(ctx context.Context, blockIssuingTime time.Time, currentAPI iotago.API) {
	protocolParametersHash, err := currentAPI.ProtocolParameters().Hash()
	if err != nil {
		Component.LogWarnf("failed to get protocol parameters hash: %s", err.Error())

		return
	}

	strongParents, weakParents, shallowLikeParents, err := deps.NodeBridge.RequestTips(ctx, iotago.BlockTypeValidationMaxParents)

	addressableCommitment, err := getAddressableCommitment(ctx, currentAPI.TimeProvider().SlotFromTime(blockIssuingTime))
	//if err != nil && ierrors.Is(err, ErrBlockTooRecent) {
	//	// TODO: is the chain going to be revived if node is not marked as bootstrapped?
	//	commitment, parentID, reviveChainErr := reviveChain(ctx, blockIssuingTime)
	//	if reviveChainErr != nil {
	//		Component.LogError("error reviving chain: %s", reviveChainErr.Error())
	//		return
	//	}
	//
	//	addressableCommitment = commitment
	//	strongParents = []iotago.BlockID{parentID}
	//} else
	if err != nil {
		Component.LogWarnf("error getting commitment: %s", err.Error())

		return
	}

	// create the validation block here using the validation block builder from iota.go
	validationBlock, err := builder.NewValidationBlockBuilder(currentAPI).
		IssuingTime(blockIssuingTime).
		ProtocolParametersHash(protocolParametersHash).
		SlotCommitmentID(addressableCommitment.MustID()).
		HighestSupportedVersion(deps.NodeBridge.APIProvider().LatestAPI().Version()).
		LatestFinalizedSlot(deps.NodeBridge.LatestFinalizedCommitmentID().Slot()).
		StrongParents(strongParents).
		WeakParents(weakParents).
		ShallowLikeParents(shallowLikeParents).
		Sign(validatorAccount.ID(), validatorAccount.PrivateKey()).
		Build()
	if err != nil {
		Component.LogWarnf("error creating validation block: %s", err.Error())

		return
	}

	blockID, err := deps.NodeBridge.SubmitBlock(ctx, validationBlock)
	if err != nil {
		Component.LogWarnf("error issuing validator block: %s", err.Error())

		return
	}

	Component.LogDebugf("issued validator block: %s - commitment %s %d - latest finalized slot %d", blockID, validationBlock.SlotCommitmentID, validationBlock.SlotCommitmentID.Slot(), validationBlock.LatestFinalizedSlot)
}

// TODO: should this be part of the node maybe?
//func reviveChain(ctx context.Context, issuingTime time.Time) (*iotago.Commitment, iotago.BlockID, error) {
//	lastCommittedSlot := deps.NodeBridge.NodeStatus().LatestCommitment.CommitmentId.Unwrap().Slot()
//	apiForSlot := deps.NodeBridge.APIProvider().APIForSlot(lastCommittedSlot)
//
//	// Get a rootblock as recent as possible for the parent.
//	parentBlockID := iotago.EmptyBlockID
//	for rootBlock := range deps.NodeBridge.ReadActiveRootBlocks() {
//		if rootBlock.Slot() > parentBlockID.Slot() {
//			parentBlockID = rootBlock
//		}
//
//		// Exit the loop if we found a rootblock in the last committed slot (which is the highest we can get).
//		if parentBlockID.Slot() == lastCommittedSlot {
//			break
//		}
//	}
//
//	issuingSlot := apiForSlot.TimeProvider().SlotFromTime(issuingTime)
//
//	// Force commitments until minCommittableAge relative to the block's issuing time. We basically "pretend" that
//	// this block was already accepted at the time of issuing so that we have a commitment to reference.
//	if issuingSlot < apiForSlot.ProtocolParameters().MinCommittableAge() { // Should never happen as we're beyond maxCommittableAge which is > minCommittableAge.
//		return nil, iotago.EmptyBlockID, ierrors.Errorf("issuing slot %d is smaller than min committable age %d", issuingSlot, apiForSlot.ProtocolParameters().MinCommittableAge())
//	}
//	commitUntilSlot := issuingSlot - apiForSlot.ProtocolParameters().MinCommittableAge()
//
//	if err := deps.NodeBridge.ForceCommitUntil(commitUntilSlot); err != nil {
//		return nil, iotago.EmptyBlockID, ierrors.Wrapf(err, "failed to force commit until slot %d", commitUntilSlot)
//	}
//
//	commitment, err := deps.NodeBridge.Commitment(ctx, commitUntilSlot)
//	if err != nil {
//		return nil, iotago.EmptyBlockID, ierrors.Wrapf(err, "failed to commit until slot %d to revive chain", commitUntilSlot)
//	}
//
//	return commitment.Commitment, parentBlockID, nil
//}

func getAddressableCommitment(ctx context.Context, blockSlot iotago.SlotIndex) (*iotago.Commitment, error) {
	protoParams := deps.NodeBridge.APIProvider().APIForSlot(blockSlot).ProtocolParameters()
	commitment, err := deps.NodeBridge.LatestCommitment()
	if err != nil {
		return nil, ierrors.Wrapf(err, "error loading latest commitment")
	}

	if blockSlot > commitment.Slot+protoParams.MaxCommittableAge() {
		return nil, ierrors.Wrapf(ErrBlockTooRecent, "can't issue block: block slot %d is too far in the future, latest commitment is %d", blockSlot, commitment.Slot)
	}

	if blockSlot < commitment.Slot+protoParams.MinCommittableAge() {
		if blockSlot < protoParams.MinCommittableAge() || commitment.Slot < protoParams.MinCommittableAge() {
			return commitment, nil
		}

		commitmentSlot := commitment.Slot - protoParams.MinCommittableAge()
		loadedCommitment, err := deps.NodeBridge.Commitment(ctx, commitmentSlot)
		if err != nil {
			return nil, ierrors.Wrapf(err, "error loading valid commitment of slot %d according to minCommittableAge", commitmentSlot)
		}

		return loadedCommitment.Commitment, nil
	}

	return commitment, nil
}
