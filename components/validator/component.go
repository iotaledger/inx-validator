package validator

import (
	"context"
	"crypto/ed25519"
	"os"
	"strings"
	"sync/atomic"

	"go.uber.org/dig"

	"github.com/iotaledger/hive.go/app"
	"github.com/iotaledger/hive.go/app/shutdown"
	"github.com/iotaledger/hive.go/crypto"
	"github.com/iotaledger/hive.go/ierrors"
	"github.com/iotaledger/hive.go/runtime/event"
	"github.com/iotaledger/hive.go/runtime/timed"
	"github.com/iotaledger/inx-app/pkg/nodebridge"
	"github.com/iotaledger/inx-validator/pkg/daemon"
	iotago "github.com/iotaledger/iota.go/v4"
)

func init() {
	Component = &app.Component{
		Name:     "Validator",
		DepsFunc: func(cDeps dependencies) { deps = cDeps },
		Params:   params,
		Provide:  provide,
		Run:      run,
	}
}

var (
	Component *app.Component
	deps      dependencies

	isValidator      atomic.Bool
	executor         *timed.TaskExecutor[TaskType]
	validatorAccount Account
)

type dependencies struct {
	dig.In

	NodeBridge      *nodebridge.NodeBridge
	AccountAddress  *iotago.AccountAddress
	PrivateKey      ed25519.PrivateKey
	ShutdownHandler *shutdown.ShutdownHandler
}

func provide(c *dig.Container) error {
	type depsIn struct {
		dig.In
		NodeBridge *nodebridge.NodeBridge
	}

	if err := c.Provide(func(deps depsIn) (*iotago.AccountAddress, error) {
		if ParamsValidator.AccountAddress == "" {
			return nil, ierrors.Errorf("failed to load account address. error: empty bech32 in config")
		}

		hrp, addr, err := iotago.ParseBech32(ParamsValidator.AccountAddress)
		if err != nil {
			return nil, ierrors.Wrapf(err, "failed to load account address. error: invalid bech32 address: %s", ParamsValidator.AccountAddress)
		}

		if deps.NodeBridge.APIProvider().CommittedAPI().ProtocolParameters().Bech32HRP() != hrp {
			return nil, ierrors.Wrapf(err, "failed to load account address. error: invalid bech32 address prefix: %s", hrp)
		}

		accountAddr, ok := addr.(*iotago.AccountAddress)
		if !ok {
			return nil, ierrors.Errorf("failed to load account address. error: invalid bech32 address, not an account: %s", ParamsValidator.AccountAddress)
		}

		return accountAddr, nil
	}); err != nil {
		return err
	}

	return c.Provide(func() (ed25519.PrivateKey, error) {
		privateKeys, err := loadEd25519PrivateKeysFromEnvironment("VALIDATOR_PRV_KEY")
		if err != nil {
			return nil, ierrors.Errorf("loading validator private key failed, err: %w", err)
		}

		if len(privateKeys) == 0 {
			return nil, ierrors.New("loading validator private key failed, err: no private keys given")
		}

		if len(privateKeys) > 1 {
			return nil, ierrors.New("loading validator private key failed, err: too many private keys given")
		}

		privateKey := privateKeys[0]
		if len(privateKey) != ed25519.PrivateKeySize {
			return nil, ierrors.New("loading validator private key failed, err: wrong private key length")
		}

		return privateKey, nil
	})
}

func run() error {
	validatorAccount = NewEd25519Account(deps.AccountAddress.AccountID(), deps.PrivateKey)

	executor = timed.NewTaskExecutor[TaskType](1)

	return Component.Daemon().BackgroundWorker(Component.Name, func(ctx context.Context) {
		Component.LogInfof("Starting Validator with IssuerID: %s", validatorAccount.ID())

		deps.NodeBridge.Events.LatestCommittedSlotChanged.Hook(func(details *nodebridge.Commitment) {
			checkValidatorStatus(ctx)
		}, event.WithWorkerPool(Component.WorkerPool))

		checkValidatorStatus(ctx)

		<-ctx.Done()

		executor.Shutdown()

		Component.LogInfo("Stopping Validator... done")
	}, daemon.PriorityStopValidator)
}

func checkValidatorStatus(ctx context.Context) {
	isAccountValidator, err := deps.NodeBridge.ReadIsValidatorAccount(ctx, validatorAccount.ID(), deps.NodeBridge.NodeStatus().LatestCommitment.CommitmentId.Unwrap().Slot())
	if err != nil {
		Component.LogErrorf("error when retrieving Validator account %s: %w", validatorAccount.ID(), err)

		return
	}

	if isAccountValidator {
		if prevValue := isValidator.Swap(false); prevValue {
			// If the account stops being a validator, don't issue any blocks.
			Component.LogInfof("validator account %s stopped being a validator", validatorAccount.ID())
			executor.Cancel(CandidateTask)
			executor.Cancel(CommitteeTask)
		}

		return
	}

	if prevValue := isValidator.Swap(true); !prevValue {
		Component.LogInfof("validator account %s became a validator", validatorAccount.ID())

		// If the account is a validator, start issuing validator blocks when the account is part of the committee.
		committeeMemberAction(ctx)

		// If the account is a validator, start issuing blocks to announce candidacy for the committee.
		candidateAction(ctx)
	}
}

// loadEd25519PrivateKeysFromEnvironment loads ed25519 private keys from the given environment variable.
func loadEd25519PrivateKeysFromEnvironment(name string) ([]ed25519.PrivateKey, error) {
	keys, exists := os.LookupEnv(name)
	if !exists {
		return nil, ierrors.Errorf("environment variable '%s' not set", name)
	}

	if len(keys) == 0 {
		return nil, ierrors.Errorf("environment variable '%s' not set", name)
	}

	privateKeysSplit := strings.Split(keys, ",")
	privateKeys := make([]ed25519.PrivateKey, len(privateKeysSplit))
	for i, key := range privateKeysSplit {
		privateKey, err := crypto.ParseEd25519PrivateKeyFromString(key)
		if err != nil {
			return nil, ierrors.Errorf("environment variable '%s' contains an invalid private key '%s'", name, key)

		}
		privateKeys[i] = privateKey
	}

	return privateKeys, nil
}
