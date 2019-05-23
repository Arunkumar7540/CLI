// Package v7pushaction contains the business logic for orchestrating a V2 app
// push.
package v7pushaction

import (
	"regexp"
)

// Warnings is a list of warnings returned back from the cloud controller
type Warnings []string

// Actor handles all business logic for Cloud Controller v2 operations.
type Actor struct {
	SharedActor SharedActor
	V2Actor     V2Actor
	V7Actor     V7Actor

	PreparePushPlanSequence   []UpdatePushPlanFunc
	ChangeApplicationSequence []ChangeApplicationFunc

	startWithProtocol *regexp.Regexp
	urlValidator      *regexp.Regexp
}

const ProtocolRegexp = "^https?://|^tcp://"
const URLRegexp = "^(?:https?://|tcp://)?(?:(?:[\\w-]+\\.)|(?:[*]\\.))+\\w+(?:\\:\\d+)?(?:/.*)*(?:\\.\\w+)?$"

// NewActor returns a new actor.
func NewActor(v2Actor V2Actor, v3Actor V7Actor, sharedActor SharedActor) *Actor {
	actor := &Actor{
		SharedActor: sharedActor,
		V2Actor:     v2Actor,
		V7Actor:     v3Actor,

		startWithProtocol: regexp.MustCompile(ProtocolRegexp),
		urlValidator:      regexp.MustCompile(URLRegexp),
	}

	actor.PreparePushPlanSequence = []UpdatePushPlanFunc{
		SetupApplicationForPushPlan,
		SetupDockerImageCredentialsForPushPlan,
		SetupBitsPathForPushPlan,
		SetupDropletPathForPushPlan,
		actor.SetupAllResourcesForPushPlan,
		SetupNoStartForPushPlan,
		SetupSkipRouteCreationForPushPlan,
		SetupScaleWebProcessForPushPlan,
		SetupUpdateWebProcessForPushPlan,
	}

	actor.ChangeApplicationSequence = []ChangeApplicationFunc{
		RunIf(ShouldUpdateApplication, actor.UpdateApplication),
		RunIf(ShouldUpdateRoutes, actor.UpdateRoutesForApplication),
		RunIf(ShouldScaleWebProcess, actor.ScaleWebProcessForApplication),
		RunIf(ShouldUpdateWebProcess, actor.UpdateWebProcessForApplication),
		RunIf(ShouldCreateBitsPackage, actor.CreateBitsPackageForApplication),
		RunIf(ShouldCreateDockerPackage, actor.CreateDockerPackageForApplication),
		RunIf(ShouldCreateDroplet, actor.CreateDropletForApplication),
		RunIf(ShouldStagePackage, actor.StagePackageForApplication),
		RunIf(ShouldStopApplication, actor.StopApplication),
		RunIf(ShouldSetDroplet, actor.SetDropletForApplication),

		//RunIf(And(DropletPathSet, Not(NoStartFlagSet)), F),
		//RunIfElse(Predicate, F1, F2),
		//RunSequence(...Fs),

		//RunIf(
		//	IsDocker, RunSequence(
		//		F1,
		//		F2,
		//		F3)),
		//RunIf(
		//	IsBits, RunSequence(
		//		F4,
		//		F5,
		//		F6)),
	}

	return actor
}
