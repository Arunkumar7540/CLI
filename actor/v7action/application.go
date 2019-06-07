package v7action

import (
	"errors"
	"time"

	"code.cloudfoundry.org/cli/actor/actionerror"
	"code.cloudfoundry.org/cli/api/cloudcontroller/ccerror"
	"code.cloudfoundry.org/cli/api/cloudcontroller/ccv3"
	"code.cloudfoundry.org/cli/api/cloudcontroller/ccv3/constant"
)

// Application represents a V3 actor application.
type Application struct {
	Name                string
	GUID                string
	StackName           string
	State               constant.ApplicationState
	LifecycleType       constant.AppLifecycleType
	LifecycleBuildpacks []string
	Metadata            *Metadata
}

func (app Application) Started() bool {
	return app.State == constant.ApplicationStarted
}

func (app Application) Stopped() bool {
	return app.State == constant.ApplicationStopped
}

func (actor Actor) DeleteApplicationByNameAndSpace(name string, spaceGUID string) (Warnings, error) {
	var allWarnings Warnings

	app, getAppWarnings, err := actor.GetApplicationByNameAndSpace(name, spaceGUID)
	allWarnings = append(allWarnings, getAppWarnings...)
	if err != nil {
		return allWarnings, err
	}

	jobURL, deleteAppWarnings, err := actor.CloudControllerClient.DeleteApplication(app.GUID)
	allWarnings = append(allWarnings, deleteAppWarnings...)
	if err != nil {
		return allWarnings, err
	}

	pollWarnings, err := actor.CloudControllerClient.PollJob(jobURL)
	allWarnings = append(allWarnings, pollWarnings...)
	return allWarnings, err
}

func (actor Actor) GetApplicationsByNamesAndSpace(appNames []string, spaceGUID string) ([]Application, Warnings, error) {
	apps, warnings, err := actor.CloudControllerClient.GetApplications(
		ccv3.Query{Key: ccv3.NameFilter, Values: appNames},
		ccv3.Query{Key: ccv3.SpaceGUIDFilter, Values: []string{spaceGUID}},
	)

	if err != nil {
		return nil, Warnings(warnings), err
	}

	if len(apps) < len(appNames) {
		return nil, Warnings(warnings), actionerror.ApplicationsNotFoundError{}
	}

	actorApps := []Application{}
	for _, a := range apps {
		actorApps = append(actorApps, actor.convertCCToActorApplication(a))
	}
	return actorApps, Warnings(warnings), nil
}

// GetApplicationByNameAndSpace returns the application with the given
// name in the given space.
func (actor Actor) GetApplicationByNameAndSpace(appName string, spaceGUID string) (Application, Warnings, error) {
	apps, warnings, err := actor.GetApplicationsByNamesAndSpace([]string{appName}, spaceGUID)

	if err != nil {
		if _, ok := err.(actionerror.ApplicationsNotFoundError); ok {
			return Application{}, warnings, actionerror.ApplicationNotFoundError{Name: appName}
		}
		return Application{}, warnings, err
	}

	return apps[0], warnings, nil
}

// GetApplicationsBySpace returns all applications in a space.
func (actor Actor) GetApplicationsBySpace(spaceGUID string) ([]Application, Warnings, error) {
	ccApps, warnings, err := actor.CloudControllerClient.GetApplications(
		ccv3.Query{Key: ccv3.SpaceGUIDFilter, Values: []string{spaceGUID}},
	)

	if err != nil {
		return []Application{}, Warnings(warnings), err
	}

	var apps []Application
	for _, ccApp := range ccApps {
		apps = append(apps, actor.convertCCToActorApplication(ccApp))
	}
	return apps, Warnings(warnings), nil
}

// CreateApplicationInSpace creates and returns the application with the given
// name in the given space.
func (actor Actor) CreateApplicationInSpace(app Application, spaceGUID string) (Application, Warnings, error) {
	createdApp, warnings, err := actor.CloudControllerClient.CreateApplication(
		ccv3.Application{
			LifecycleType:       app.LifecycleType,
			LifecycleBuildpacks: app.LifecycleBuildpacks,
			StackName:           app.StackName,
			Name:                app.Name,
			Relationships: ccv3.Relationships{
				constant.RelationshipTypeSpace: ccv3.Relationship{GUID: spaceGUID},
			},
		})

	if err != nil {
		if _, ok := err.(ccerror.NameNotUniqueInSpaceError); ok {
			return Application{}, Warnings(warnings), actionerror.ApplicationAlreadyExistsError{Name: app.Name}
		}
		return Application{}, Warnings(warnings), err
	}

	return actor.convertCCToActorApplication(createdApp), Warnings(warnings), nil
}

// SetApplicationProcessHealthCheckTypeByNameAndSpace sets the health check
// information of the provided processType for an application with the given
// name and space GUID.
func (actor Actor) SetApplicationProcessHealthCheckTypeByNameAndSpace(
	appName string,
	spaceGUID string,
	healthCheckType constant.HealthCheckType,
	httpEndpoint string,
	processType string,
	invocationTimeout int64,
) (Application, Warnings, error) {

	app, getWarnings, err := actor.GetApplicationByNameAndSpace(appName, spaceGUID)
	if err != nil {
		return Application{}, getWarnings, err
	}

	setWarnings, err := actor.UpdateProcessByTypeAndApplication(
		processType,
		app.GUID,
		Process{
			HealthCheckType:              healthCheckType,
			HealthCheckEndpoint:          httpEndpoint,
			HealthCheckInvocationTimeout: invocationTimeout,
		})
	return app, append(getWarnings, setWarnings...), err
}

// StopApplication stops an application.
func (actor Actor) StopApplication(appGUID string) (Warnings, error) {
	_, warnings, err := actor.CloudControllerClient.UpdateApplicationStop(appGUID)

	return Warnings(warnings), err
}

// StartApplication starts an application.
func (actor Actor) StartApplication(appGUID string) (Application, Warnings, error) {
	updatedApp, warnings, err := actor.CloudControllerClient.UpdateApplicationStart(appGUID)
	if err != nil {
		return Application{}, Warnings(warnings), err
	}

	return actor.convertCCToActorApplication(updatedApp), Warnings(warnings), nil
}

// RestartApplication restarts an application and waits for it to start.
func (actor Actor) RestartApplication(appGUID string) (Warnings, error) {
	var allWarnings Warnings
	_, warnings, err := actor.CloudControllerClient.UpdateApplicationRestart(appGUID)
	allWarnings = append(allWarnings, warnings...)
	if err != nil {
		return allWarnings, err
	}

	pollingWarnings, err := actor.PollStart(appGUID)
	allWarnings = append(allWarnings, pollingWarnings...)
	return allWarnings, err
}

func (actor Actor) PollStart(appGUID string) (Warnings, error) {
	processes, warnings, err := actor.CloudControllerClient.GetApplicationProcesses(appGUID)
	return actor.pollForProcesses(processes, warnings, err)
}

func (actor Actor) PollStartForRolling(appGUID string, deploymentGUID string) (Warnings, error) {

	var allWarnings Warnings

	processes, warnings, err := actor.CloudControllerClient.GetNewApplicationProcesses(appGUID, deploymentGUID)
	allWarnings = append(allWarnings, warnings...)

	v3warnings, err := actor.pollForProcesses(processes, warnings, err)
	allWarnings = append(allWarnings, v3warnings...)
	if err != nil {
		return allWarnings, err
	}

	allProcesses, warnings, err := actor.CloudControllerClient.GetApplicationProcesses(appGUID)
	allWarnings = append(allWarnings, warnings...)
	if err != nil {
		return allWarnings, err
	}
	nonWebProcesses := []ccv3.Process{}
	for _, process := range allProcesses {
		if process.Type != constant.ProcessTypeWeb {
			nonWebProcesses = append(nonWebProcesses, process)
		}
	}
	pollingWarnings, err := actor.pollForProcesses(nonWebProcesses, warnings, err)
	allWarnings = append(allWarnings, pollingWarnings...)
	return allWarnings, nil
}

func (actor Actor) BadPollStartForRolling(appGUID string, deploymentGUID string) (Warnings, error) {
	deploymentWarnings, err := actor.pollDeployment(deploymentGUID)
	var allWarnings Warnings
	allWarnings = append(allWarnings, deploymentWarnings...)
	if err != nil {
		//if _, ok := err.(actionerror.StartupTimeoutError); ok {
		//	err = nil
		//}
		allWarnings = append(allWarnings, err.Error())
		return allWarnings, nil
	}
	// There are no temporary WEB-like processes at this point. So we can ignore WEB processes
	allProcesses, warnings, err := actor.CloudControllerClient.GetApplicationProcesses(appGUID)

	allWarnings = append(allWarnings, warnings...)
	if err != nil {
		return allWarnings, err
	}
	nonWebProcesses := []ccv3.Process{}
	for _, process := range allProcesses {
		if process.Type != constant.ProcessTypeWeb {
			nonWebProcesses = append(nonWebProcesses, process)
		}
	}
	pollingWarnings, err := actor.pollForProcesses(nonWebProcesses, warnings, err)
	if err != nil {
		allWarnings = append(allWarnings, err.Error())
	}
	allWarnings = append(allWarnings, pollingWarnings...)
	//if _, ok := err.(actionerror.StartupTimeoutError); ok {
	//	err = nil
	//}
	return allWarnings, nil
}

func (actor Actor) getDeploymentState(deploymentGUID string) (constant.DeploymentState, Warnings, error) {
	deployment, warnings, err := actor.CloudControllerClient.GetDeployment(deploymentGUID)
	if err != nil {
		return "", Warnings(warnings), err
	}
	return deployment.State, Warnings(warnings), nil
}

func (actor Actor) pollDeployment(deploymentGUID string) (Warnings, error) {
	var allWarnings Warnings

	timeout := time.Now().Add(actor.Config.StartupTimeout())
	for time.Now().Before(timeout) {
		deploymentState, warnings, err := actor.getDeploymentState(deploymentGUID)
		allWarnings = append(allWarnings, warnings...)
		if err != nil {
			return allWarnings, err
		}
		switch deploymentState {
		case constant.DeploymentDeployed:
			return allWarnings, nil
		case constant.DeploymentCanceled:
			return allWarnings, errors.New("Deployment has been canceled")
		case constant.DeploymentFailed:
			return allWarnings, errors.New("Deployment has failed")
		case constant.DeploymentDeploying:
		case constant.DeploymentFailing:
		case constant.DeploymentCanceling:
			time.Sleep(actor.Config.PollingInterval())
		}
	}
	return allWarnings, actionerror.StartupTimeoutError{}
}

// UpdateApplication updates the buildpacks on an application
func (actor Actor) UpdateApplication(app Application) (Application, Warnings, error) {
	ccApp := ccv3.Application{
		GUID:                app.GUID,
		StackName:           app.StackName,
		LifecycleType:       app.LifecycleType,
		LifecycleBuildpacks: app.LifecycleBuildpacks,
		Metadata:            (*ccv3.Metadata)(app.Metadata),
	}

	updatedApp, warnings, err := actor.CloudControllerClient.UpdateApplication(ccApp)
	if err != nil {
		return Application{}, Warnings(warnings), err
	}

	return actor.convertCCToActorApplication(updatedApp), Warnings(warnings), nil
}

func (Actor) convertCCToActorApplication(app ccv3.Application) Application {
	return Application{
		GUID:                app.GUID,
		StackName:           app.StackName,
		LifecycleType:       app.LifecycleType,
		LifecycleBuildpacks: app.LifecycleBuildpacks,
		Name:                app.Name,
		State:               app.State,
		Metadata:            (*Metadata)(app.Metadata),
	}
}

func (actor Actor) pollForProcesses(processes []ccv3.Process, warnings ccv3.Warnings, err error) (Warnings, error) {
	var allWarnings Warnings
	allWarnings = append(allWarnings, warnings...)
	if err != nil {
		return allWarnings, err
	}

	timeout := time.Now().Add(actor.Config.StartupTimeout())
	for time.Now().Before(timeout) {
		allProcessesDone := true
		for _, process := range processes {
			shouldContinuePolling, warnings, err := actor.shouldContinuePollingProcessStatus(process)
			allWarnings = append(allWarnings, warnings...)
			if err != nil {
				return allWarnings, err
			}

			if shouldContinuePolling {
				allProcessesDone = false
				break
			}
		}

		if allProcessesDone {
			return allWarnings, nil
		}
		time.Sleep(actor.Config.PollingInterval())
	}

	return allWarnings, actionerror.StartupTimeoutError{}
}

func (actor Actor) shouldContinuePollingProcessStatus(process ccv3.Process) (bool, Warnings, error) {
	ccInstances, ccWarnings, err := actor.CloudControllerClient.GetProcessInstances(process.GUID)
	instances := ProcessInstances(ccInstances)
	warnings := Warnings(ccWarnings)
	if err != nil {
		return true, warnings, err
	}

	if instances.Empty() || instances.AnyRunning() {
		return false, warnings, nil
	} else if instances.AllCrashed() {
		return false, warnings, actionerror.AllInstancesCrashedError{}
	}

	return true, warnings, nil
}
