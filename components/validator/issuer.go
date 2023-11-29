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
	now := time.Now()
	currentAPI := deps.NodeBridge.APIProvider().APIForTime(now)
	currentSlot := currentAPI.TimeProvider().SlotFromTime(now)

	isCandidate, err := deps.NodeBridge.ReadIsCandidate(ctx, validatorAccount.ID(), currentSlot)
	if err != nil {
		Component.LogWarnf("error while checking if account is already a candidate: %s", err.Error())
		// If there is an error, then retry registering as a candidate.
		executor.ExecuteAt(CandidateTask, func() { candidateAction(ctx) }, now.Add(ParamsValidator.CandidacyRetryInterval))

		return
	}

	if isCandidate {
		Component.LogDebugf("not issuing candidacy announcement block as account %s is already registered as candidate in epoch %d", validatorAccount.ID(), currentAPI.TimeProvider().EpochFromSlot(currentSlot))
		// If an account is a registered candidate, then try to register as a candidate in the next epoch.
		executor.ExecuteAt(CandidateTask, func() { candidateAction(ctx) }, currentAPI.TimeProvider().SlotStartTime(currentAPI.TimeProvider().EpochStart(currentAPI.TimeProvider().EpochFromSlot(currentSlot)+1)))

		return
	}

	epochEndSlot := currentAPI.TimeProvider().EpochEnd(currentAPI.TimeProvider().EpochFromSlot(currentSlot))
	if currentSlot+currentAPI.ProtocolParameters().EpochNearingThreshold() > epochEndSlot {
		Component.LogDebugf("not issuing candidacy announcement for account %s as block's slot would be issued in (%d) is past the Epoch Nearing Threshold (%d) of epoch %d", validatorAccount.ID(), currentSlot, epochEndSlot-currentAPI.ProtocolParameters().EpochNearingThreshold(), currentAPI.TimeProvider().EpochFromSlot(currentSlot))
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
	executor.ExecuteAt(CandidateTask, func() { candidateAction(ctx) }, now.Add(ParamsValidator.CandidacyRetryInterval))

	if !deps.NodeBridge.NodeStatus().GetIsBootstrapped() {
		Component.LogDebug("not issuing candidate block because node is not bootstrapped yet.")

		return
	}

	if err = issueCandidateBlock(ctx, now, currentAPI); err != nil {
		Component.LogWarnf("error while trying to issue candidacy announcement: %s", err.Error())
	}
}

func committeeMemberAction(ctx context.Context) {
	now := time.Now()
	currentAPI := deps.NodeBridge.APIProvider().APIForTime(now)
	currentSlot := currentAPI.TimeProvider().SlotFromTime(now)
	currentEpoch := currentAPI.TimeProvider().EpochFromSlot(currentSlot)
	slotDurationMillis := int64(currentAPI.ProtocolParameters().SlotDurationInSeconds()) * 1000
	// Calculate the broadcast interval in milliseconds.
	broadcastIntervalMillis := slotDurationMillis / int64(currentAPI.ProtocolParameters().ValidationBlocksPerSlot())
	committeeBroadcastInterval := time.Duration(broadcastIntervalMillis * int64(time.Millisecond))

	// If we are bootstrapped let's check if we are part of the committee.
	if deps.NodeBridge.NodeStatus().GetIsBootstrapped() {
		isCommitteeMember, err := deps.NodeBridge.ReadIsCommitteeMember(ctx, validatorAccount.ID(), currentSlot)
		if err != nil {
			Component.LogWarnf("error while checking if account %s is a committee member in slot %d: %s", validatorAccount.ID(), currentSlot, err.Error())
			executor.ExecuteAt(CommitteeTask, func() { committeeMemberAction(ctx) }, now.Add(committeeBroadcastInterval))

			return
		}

		if !isCommitteeMember {
			Component.LogDebug("account %s is not a committee member in epoch %d", currentEpoch)
			executor.ExecuteAt(CommitteeTask, func() { committeeMemberAction(ctx) }, currentAPI.TimeProvider().SlotStartTime(currentAPI.TimeProvider().EpochStart(currentEpoch+1)))

			return
		}
	}

	// Schedule next committeeMemberAction regardless of whether the node is bootstrapped or validation block is issued
	// as it must be issued as part of validator's responsibility.
	executor.ExecuteAt(CommitteeTask, func() { committeeMemberAction(ctx) }, now.Add(committeeBroadcastInterval))

	// If we are not bootstrapped and we are _not_ ignoring such condition, we return.
	if !deps.NodeBridge.NodeStatus().GetIsBootstrapped() && !ParamsValidator.IgnoreBootstrapped {
		Component.LogDebug("not issuing validation block because node is not bootstrapped yet.")

		return
	}

	// If we are either bootstrapped (and we are part of the committee) or we are ignoring being bootstrapped we issue
	// a validation block, reviving the chain if necessary.
	if err := issueValidationBlock(ctx, now, currentAPI, committeeBroadcastInterval); err != nil {
		Component.LogWarnf("error while trying to issue validation block: %s", err.Error())
	}
}

func issueCandidateBlock(ctx context.Context, blockIssuingTime time.Time, currentAPI iotago.API) error {
	blockSlot := currentAPI.TimeProvider().SlotFromTime(blockIssuingTime)

	strongParents, weakParents, shallowLikeParents, err := deps.NodeBridge.RequestTips(ctx, iotago.BasicBlockMaxParents)
	if err != nil {
		return ierrors.Wrapf(err, "failed to get tips")
	}

	addressableCommitment, err := getAddressableCommitment(ctx, blockSlot)
	if err != nil {
		return ierrors.Wrap(err, "error getting commitment")
	}

	candidacyBlock, err := builder.NewBasicBlockBuilder(currentAPI).
		IssuingTime(blockIssuingTime).
		SlotCommitmentID(addressableCommitment.MustID()).
		LatestFinalizedSlot(deps.NodeBridge.LatestFinalizedCommitment().Commitment.Slot).
		StrongParents(strongParents).
		WeakParents(weakParents).
		ShallowLikeParents(shallowLikeParents).
		Payload(&iotago.CandidacyAnnouncement{}).
		CalculateAndSetMaxBurnedMana(addressableCommitment.ReferenceManaCost).
		Sign(validatorAccount.ID(), validatorAccount.PrivateKey()).
		Build()
	if err != nil {
		return ierrors.Wrap(err, "error creating block")
	}

	blockID, err := deps.NodeBridge.SubmitBlock(ctx, candidacyBlock)
	if err != nil {
		return ierrors.Wrap(err, "error issuing candidacy announcement block")
	}

	Component.LogDebugf("issued candidacy announcement block: %s - commitment %s %d - latest finalized slot %d", blockID, candidacyBlock.Header.SlotCommitmentID, candidacyBlock.Header.SlotCommitmentID.Slot(), candidacyBlock.Header.LatestFinalizedSlot)

	return nil
}

func issueValidationBlock(ctx context.Context, blockIssuingTime time.Time, currentAPI iotago.API, committeeBroadcastInterval time.Duration) error {
	protocolParametersHash, err := currentAPI.ProtocolParameters().Hash()
	if err != nil {
		return ierrors.Wrapf(err, "failed to get protocol parameters hash")
	}

	strongParents, weakParents, shallowLikeParents, err := deps.NodeBridge.RequestTips(ctx, iotago.ValidationBlockMaxParents)
	if err != nil {
		return ierrors.Wrapf(err, "failed to get tips")
	}

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
			return ierrors.Wrapf(err, "error reviving chain")
		}

		addressableCommitment = commitment
		strongParents = []iotago.BlockID{parentID}
	} else if err != nil {
		return ierrors.Wrapf(err, "error getting commitment")
	}

	// create the validation block here using the validation block builder from iota.go
	validationBlock, err := builder.NewValidationBlockBuilder(currentAPI).
		IssuingTime(blockIssuingTime).
		ProtocolParametersHash(protocolParametersHash).
		SlotCommitmentID(addressableCommitment.MustID()).
		HighestSupportedVersion(deps.NodeBridge.APIProvider().LatestAPI().Version()).
		LatestFinalizedSlot(deps.NodeBridge.LatestFinalizedCommitment().Commitment.Slot).
		StrongParents(strongParents).
		WeakParents(weakParents).
		ShallowLikeParents(shallowLikeParents).
		Sign(validatorAccount.ID(), validatorAccount.PrivateKey()).
		Build()
	if err != nil {
		return ierrors.Wrapf(err, "error creating validation block")
	}

	blockID, err := deps.NodeBridge.SubmitBlock(ctx, validationBlock)
	if err != nil {
		return ierrors.Wrapf(err, "error issuing validation block")
	}

	Component.LogDebugf("issued validation block: %s - commitment %s %d - latest finalized slot %d - broadcast interval %dms", blockID, validationBlock.Header.SlotCommitmentID, validationBlock.Header.SlotCommitmentID.Slot(), validationBlock.Header.LatestFinalizedSlot, committeeBroadcastInterval.Milliseconds())

	return nil
}

func reviveChain(ctx context.Context, issuingTime time.Time) (*iotago.Commitment, iotago.BlockID, error) {
	lastCommittedSlot := deps.NodeBridge.NodeStatus().LatestCommitment.CommitmentId.Unwrap().Slot()
	apiForSlot := deps.NodeBridge.APIProvider().APIForSlot(lastCommittedSlot)

	activeRootBlocks, err := deps.NodeBridge.ActiveRootBlocks(ctx)
	if err != nil {
		return nil, iotago.EmptyBlockID, ierrors.Wrapf(err, "failed to retrieve active root blocks")
	}

	// Get a rootblock as recent as possible for the parent.
	parentBlockID := deps.NodeBridge.APIProvider().APIForTime(issuingTime).ProtocolParameters().GenesisBlockID()
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
	commitment := deps.NodeBridge.LatestCommitment().Commitment

	if blockSlot > commitment.Slot+protoParams.MaxCommittableAge() {
		return nil, ierrors.Wrapf(ErrBlockTooRecent, "can't issue block: block slot %d is too far in the future, latest commitment is %d", blockSlot, commitment.Slot)
	}

	if blockSlot < commitment.Slot+protoParams.MinCommittableAge() {
		if blockSlot < protoParams.GenesisSlot()+protoParams.MinCommittableAge() || commitment.Slot < protoParams.GenesisSlot()+protoParams.MinCommittableAge() {
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
