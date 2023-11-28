package validator

import (
	"time"

	"github.com/iotaledger/hive.go/app"
)

// ParametersValidator contains the definition of the configuration parameters used by the Validator component.
type ParametersValidator struct {
	// CommitteeBroadcastInterval the interval at which the node will broadcast its committee validator block.
	CommitteeBroadcastInterval time.Duration `default:"500ms" usage:"the interval at which committee validator block will be broadcast"`
	// CandidacyRetryInterval the interval at which broadcast of candidacy announcement block will be retried
	CandidacyRetryInterval time.Duration `default:"10s" usage:"the interval at which broadcast of candidacy announcement block will be retried"`
	// IssueCandidacyPayload sets whether the node should issue a candidacy payload.
	IssueCandidacyPayload bool `default:"true" usage:"whether the node should issue a candidacy payload"`
	// IgnoreBootstrapped sets whether the Validator component should start issuing validator blocks before the main engine is bootstrapped.
	IgnoreBootstrapped bool `default:"false" usage:"whether the Validator component should start issuing validator blocks before the main engine is bootstrapped"`
	// AccountAddress is the address of the account that is used to issue the blocks.
	AccountAddress string `default:"" usage:"the account address of the validator account that will issue the blocks"`
}

// ParamsValidator contains the values of the configuration parameters used by the Activity component.
var ParamsValidator = &ParametersValidator{}

var params = &app.ComponentParams{
	Params: map[string]any{
		"validator": ParamsValidator,
	},
	Masked: nil,
}
