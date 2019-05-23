package v7pushaction

import (
	"fmt"

	"code.cloudfoundry.org/cli/actor/sharedaction"
	"code.cloudfoundry.org/cli/actor/v7action"
	"code.cloudfoundry.org/cli/api/cloudcontroller/ccv3/constant"
	"code.cloudfoundry.org/cli/types"
)

type PushPlan struct {
	SpaceGUID string
	OrgGUID   string

	Application            v7action.Application
	ApplicationNeedsUpdate bool

	NoStart           bool
	NoRouteFlag       bool
	SkipRouteCreation bool

	DockerImageCredentials            v7action.DockerImageCredentials
	DockerImageCredentialsNeedsUpdate bool

	ScaleWebProcess            v7action.Process
	ScaleWebProcessNeedsUpdate bool

	UpdateWebProcess            v7action.Process
	UpdateWebProcessNeedsUpdate bool

	Manifest []byte

	Archive      bool
	BitsPath     string
	DropletPath  string
	AllResources []sharedaction.V3Resource

	PackageGUID string
	DropletGUID string
}

type FlagOverrides struct {
	Buildpacks          []string
	Stack               string
	Disk                types.NullUint64
	DropletPath         string
	DockerImage         string
	DockerPassword      string
	DockerUsername      string
	HealthCheckEndpoint string
	HealthCheckTimeout  int64
	HealthCheckType     constant.HealthCheckType
	Instances           types.NullInt
	Memory              types.NullUint64
	NoStart             bool
	ProvidedAppPath     string
	SkipRouteCreation   bool
	StartCommand        types.FilteredString
}

func (state PushPlan) String() string {
	return fmt.Sprintf(
		"Application: %#v - Space GUID: %s, Org GUID: %s, Archive: %t, Bits Path: %s",
		state.Application,
		state.SpaceGUID,
		state.OrgGUID,
		state.Archive,
		state.BitsPath,
	)
}

func RunIf(condition func(PushPlan) bool, changeFunc ChangeApplicationFunc) ChangeApplicationFunc {
	return func(plan PushPlan, eventStream chan<- Event, progressBar ProgressBar) (PushPlan, Warnings, error) {
		if condition(plan) {
			return changeFunc(plan, eventStream, progressBar)
		}

		return plan, nil, nil
	}
}

func ShouldUpdateApplication(plan PushPlan) bool {
	return plan.ApplicationNeedsUpdate
}

func ShouldUpdateRoutes(plan PushPlan) bool {
	return !(plan.SkipRouteCreation || plan.NoRouteFlag)
}

func ShouldScaleWebProcess(plan PushPlan) bool {
	return plan.ScaleWebProcessNeedsUpdate
}

func ShouldUpdateWebProcess(plan PushPlan) bool {
	return plan.UpdateWebProcessNeedsUpdate
}

func ShouldCreateBitsPackage(plan PushPlan) bool {
	return plan.DropletPath == "" && !plan.DockerImageCredentialsNeedsUpdate
}

func ShouldCreateDockerPackage(plan PushPlan) bool {
	return plan.DropletPath == "" && plan.DockerImageCredentialsNeedsUpdate
}

func ShouldCreateDroplet(plan PushPlan) bool {
	return plan.DropletPath != ""
}

func ShouldStagePackage(plan PushPlan) bool {
	return !plan.NoStart && plan.DropletPath == ""
}

func ShouldStopApplication(plan PushPlan) bool {
	return plan.NoStart && plan.Application.State == constant.ApplicationStarted
}

func ShouldSetDroplet(plan PushPlan) bool {
	return !plan.NoStart || plan.DropletPath != ""
}
