package app

import (
	"github.com/iotaledger/hive.go/app"
	"github.com/iotaledger/hive.go/app/components/profiling"
	"github.com/iotaledger/hive.go/app/components/shutdown"
	"github.com/iotaledger/inx-app/components/inx"
	"github.com/iotaledger/inx-validator/components/validator"
)

var (
	// Name of the app.
	Name = "inx-validator"

	// Version of the app.
	Version = "1.0.0-alpha.1"
)

func App() *app.App {
	return app.New(Name, Version,
		app.WithInitComponent(InitComponent),
		app.WithComponents(
			inx.Component,
			validator.Component,
			shutdown.Component,
			profiling.Component,
		),
	)
}

var (
	InitComponent *app.InitComponent
)

func init() {
	InitComponent = &app.InitComponent{
		Component: &app.Component{
			Name: "App",
		},
		NonHiddenFlags: []string{
			"config",
			"help",
			"version",
		},
	}
}
