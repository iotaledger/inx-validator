package validator

import (
	"context"
	"time"

	"github.com/iotaledger/hive.go/ierrors"
	iotago "github.com/iotaledger/iota.go/v4"
	"github.com/iotaledger/iota.go/v4/builder"
)

var ErrBlockTooRecent = ierrors.New("block is too recent compared to latest commitment")

type TaskType uint8

const (
	CandidateTask TaskType = iota
	CommitteeTask
)

func candidateAction(ctx context.Context) {
	blockIssuingTime := time.Now()
	currentAPI := deps.NodeBridge.APIProvider().APIForTime(blockIssuingTime)
	currentSlot := currentAPI.TimeProvider().SlotFromTime(blockIssuingTime)

	isCandidate, err := deps.NodeBridge.ReadIsCandidate(ctx, validatorAccount.ID(), currentSlot)
	if err != nil {
		Component.LogWarnf("error while checking if account is already a candidate: %s", err.Error())
		// If there is an error, then retry registering as a candidate.
		executor.ExecuteAt(CandidateTask, func() { candidateAction(ctx) }, blockIssuingTime.Add(ParamsValidator.CandidacyRetryInterval))

		return
	}

	if isCandidate {
		Component.LogDebugf("not issuing candidacy announcement block as account %s is already registered as candidate in epoch %d", validatorAccount.ID(), currentAPI.TimeProvider().EpochFromSlot(currentSlot))
		// If an account is a registered candidate, then try to register as a candidate in the next epoch.
		executor.ExecuteAt(CandidateTask, func() { candidateAction(ctx) }, currentAPI.TimeProvider().SlotStartTime(currentAPI.TimeProvider().EpochStart(currentAPI.TimeProvider().EpochFromSlot(currentSlot)+1)))

		return
	}

	if epochNearingThresholdSlot := currentAPI.TimeProvider().EpochEnd(currentAPI.TimeProvider().EpochFromSlot(currentSlot)) - currentAPI.ProtocolParameters().EpochNearingThreshold(); currentSlot > epochNearingThresholdSlot {
		Component.LogDebugf("not issuing candidacy announcement for account %s as the slot the block would be issued in (%d) is past the Epoch Nearing Threshold (%d)", validatorAccount.ID(), currentSlot, epochNearingThresholdSlot)
		// If it's too late to register as a candidate, then try to register in the next epoch.
		executor.ExecuteAt(CandidateTask, func() { candidateAction(ctx) }, currentAPI.TimeProvider().SlotStartTime(currentAPI.TimeProvider().EpochStart(currentAPI.TimeProvider().EpochFromSlot(currentSlot)+1)))

		return
	}

	// Schedule next committeeMemberAction regardless of the result below.
	// If a node is not bootstrapped, then retry until it is bootstrapped.
	// The candidacy block might be issued and fail to be accepted in the node for various reasons,
	// so we should stop issuing candidacy blocks only when the account is successfully registered as a candidate.
	// For this reason,
	// retry interval parameter should be bigger than the fishing threshold to avoid issuing redundant candidacy blocks.
	executor.ExecuteAt(CandidateTask, func() { candidateAction(ctx) }, blockIssuingTime.Add(ParamsValidator.CandidacyRetryInterval))

	if !deps.NodeBridge.NodeStatus().GetIsBootstrapped() {
		Component.LogDebug("not issuing candidate block because node is not bootstrapped yet.")

		return
	}

	if err := issueCandidateBlock(ctx, blockIssuingTime, currentAPI); err != nil {
		Component.LogWarnf("error while trying to issue candidacy announcement: %s", err.Error())
	}

}

func committeeMemberAction(ctx context.Context) {
	now := time.Now()
	currentAPI := deps.NodeBridge.APIProvider().APIForTime(now)
	currentSlot := currentAPI.TimeProvider().SlotFromTime(now)
	currentEpoch := currentAPI.TimeProvider().EpochFromSlot(currentSlot)

	isCommitteeMember, err := deps.NodeBridge.ReadIsCommitteeMember(ctx, validatorAccount.ID(), currentSlot)
	if err != nil {
		Component.LogWarnf("error while checking if account %s is a committee member in slot %d: %s", validatorAccount.ID(), currentSlot, err.Error())
		executor.ExecuteAt(CommitteeTask, func() { committeeMemberAction(ctx) }, now.Add(ParamsValidator.CommitteeBroadcastInterval))

		return
	}

	if !isCommitteeMember {
		Component.LogDebug("account %s is not a committee member in epoch %d", currentEpoch)
		executor.ExecuteAt(CommitteeTask, func() { committeeMemberAction(ctx) }, currentAPI.TimeProvider().SlotStartTime(currentAPI.TimeProvider().EpochStart(currentEpoch+1)))

		return
	}

	// Schedule next committeeMemberAction regardless of whether the node is bootstrapped or validator block is issued
	// as it must be issued as part of validator's responsibility.
	executor.ExecuteAt(CommitteeTask, func() { committeeMemberAction(ctx) }, now.Add(ParamsValidator.CommitteeBroadcastInterval))

	// Validator block may ignore the bootstrap flag in order to bootstrap the network and begin acceptance.
	if !ParamsValidator.IgnoreBootstrapped && !deps.NodeBridge.NodeStatus().GetIsBootstrapped() {
		Component.LogDebug("not issuing validator block because node is not bootstrapped yet.")

		return
	}

	issueValidatorBlock(ctx, now, currentAPI)
}

func issueCandidateBlock(ctx context.Context, blockIssuingTime time.Time, currentAPI iotago.API) error {
	blockSlot := currentAPI.TimeProvider().SlotFromTime(blockIssuingTime)

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
	if err != nil && ierrors.Is(err, ErrBlockTooRecent) {
		// If both validator plugin and the node it's connected to crash and restart at the same time,
		// then revival will not be possible if `IgnoreBootstrapFlag` is not set,
		// because the validator will only issue blocks if the flag is set OR the node is bootstrapped
		// (which will not happen, because the node restarted and reset its bootstrapped flag).
		// In such case, a manual intervention is necessary by the operator to set the `IgnoreBootstrappedFlag`
		// to allow the validator to issue validation blocks.
		commitment, parentID, reviveChainErr := reviveChain(ctx, blockIssuingTime)
		if reviveChainErr != nil {
			Component.LogError("error reviving chain: %s", reviveChainErr.Error())
			return
		}

		addressableCommitment = commitment
		strongParents = []iotago.BlockID{parentID}
	} else if err != nil {
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
func reviveChain(ctx context.Context, issuingTime time.Time) (*iotago.Commitment, iotago.BlockID, error) {
	lastCommittedSlot := deps.NodeBridge.NodeStatus().LatestCommitment.CommitmentId.Unwrap().Slot()
	apiForSlot := deps.NodeBridge.APIProvider().APIForSlot(lastCommittedSlot)

	activeRootBlocks, err := deps.NodeBridge.ActiveRootBlocks(ctx)
	if err != nil {
		return nil, iotago.EmptyBlockID, ierrors.Wrapf(err, "failed to retrieve active root blocks")
	}

	// Get a rootblock as recent as possible for the parent.
	parentBlockID := iotago.EmptyBlockID
	for rootBlock := range activeRootBlocks {
		if rootBlock.Slot() > parentBlockID.Slot() {
			parentBlockID = rootBlock
		}

		// Exit the loop if we found a rootblock in the last committed slot (which is the highest we can get).
		if parentBlockID.Slot() == lastCommittedSlot {
			break
		}
	}

	issuingSlot := apiForSlot.TimeProvider().SlotFromTime(issuingTime)

	// Force commitments until minCommittableAge relative to the block's issuing time. We basically "pretend" that
	// this block was already accepted at the time of issuing so that we have a commitment to reference.
	if issuingSlot < apiForSlot.ProtocolParameters().MinCommittableAge() { // Should never happen as we're beyond maxCommittableAge which is > minCommittableAge.
		return nil, iotago.EmptyBlockID, ierrors.Errorf("issuing slot %d is smaller than min committable age %d", issuingSlot, apiForSlot.ProtocolParameters().MinCommittableAge())
	}
	commitUntilSlot := issuingSlot - apiForSlot.ProtocolParameters().MinCommittableAge()

	if err = deps.NodeBridge.ForceCommitUntil(ctx, commitUntilSlot); err != nil {
		return nil, iotago.EmptyBlockID, ierrors.Wrapf(err, "failed to force commit until slot %d", commitUntilSlot)
	}

	commitment, err := deps.NodeBridge.Commitment(ctx, commitUntilSlot)
	if err != nil {
		return nil, iotago.EmptyBlockID, ierrors.Wrapf(err, "failed to commit until slot %d to revive chain", commitUntilSlot)
	}

	return commitment.Commitment, parentBlockID, nil
}

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
