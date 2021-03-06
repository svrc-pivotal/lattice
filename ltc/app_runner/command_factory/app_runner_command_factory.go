package command_factory

import (
	"errors"
	"fmt"
	"io/ioutil"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudfoundry-incubator/lattice/ltc/app_examiner"
	"github.com/cloudfoundry-incubator/lattice/ltc/app_runner"
	"github.com/cloudfoundry-incubator/lattice/ltc/exit_handler"
	"github.com/cloudfoundry-incubator/lattice/ltc/exit_handler/exit_codes"
	"github.com/cloudfoundry-incubator/lattice/ltc/logs/console_tailed_logs_outputter"
	"github.com/cloudfoundry-incubator/lattice/ltc/route_helpers"
	"github.com/cloudfoundry-incubator/lattice/ltc/terminal"
	"github.com/cloudfoundry-incubator/lattice/ltc/terminal/colors"
	"github.com/codegangsta/cli"
	"github.com/pivotal-golang/clock"
	"github.com/pivotal-golang/lager"
)

const (
	InvalidPortErrorMessage          = "Invalid port specified. Ports must be a positive integer less than 65536."
	ReservedPortErrorMessage         = "Port %d is reserved by Lattice.\nSee: http://lattice.cf/docs/troubleshooting#what-external-ports-are-unavailable-to-bind-as-tcp-routes"
	MalformedRouteErrorMessage       = "Malformed route. Routes must be of the format port:route"
	MalformedTcpRouteErrorMessage    = "Malformed TCP route. A TCP Route must be of the format container_Port:external_port"
	MustSetMonitoredPortErrorMessage = "Must set monitor-port when specifying multiple exposed ports unless --no-monitor is set."
	MonitorPortNotExposed            = "Must have an exposed port that matches the monitored port"

	DefaultPollingTimeout time.Duration = 2 * time.Minute

	pollingStart pollingAction = "start"
	pollingScale pollingAction = "scale"
)

var reservedPorts []uint16 = []uint16{
	22, 53, 80, 1169, 1234, 1700, 2222, 2380, 4001, 4222, 4223, 7001, 7777,
	8070, 8080, 8081, 8082, 8090, 8300, 8301, 8302, 8400, 8444, 8500, 8888,
	8889, 9016, 9999, 17009, 17014, 17110, 17111, 17222, 44445,
}

type pollingAction string

type AppRunnerCommandFactory struct {
	AppRunner           app_runner.AppRunner
	AppExaminer         app_examiner.AppExaminer
	UI                  terminal.UI
	Domain              string
	Env                 []string
	Clock               clock.Clock
	TailedLogsOutputter console_tailed_logs_outputter.TailedLogsOutputter
	ExitHandler         exit_handler.ExitHandler
}

type AppRunnerCommandFactoryConfig struct {
	AppRunner           app_runner.AppRunner
	AppExaminer         app_examiner.AppExaminer
	UI                  terminal.UI
	Domain              string
	Env                 []string
	Clock               clock.Clock
	Logger              lager.Logger
	TailedLogsOutputter console_tailed_logs_outputter.TailedLogsOutputter
	ExitHandler         exit_handler.ExitHandler
}

func NewAppRunnerCommandFactory(config AppRunnerCommandFactoryConfig) *AppRunnerCommandFactory {
	return &AppRunnerCommandFactory{
		AppRunner:           config.AppRunner,
		AppExaminer:         config.AppExaminer,
		UI:                  config.UI,
		Domain:              config.Domain,
		Env:                 config.Env,
		Clock:               config.Clock,
		TailedLogsOutputter: config.TailedLogsOutputter,
		ExitHandler:         config.ExitHandler,
	}
}

func (factory *AppRunnerCommandFactory) MakeSubmitLrpCommand() cli.Command {
	var submitLrpCommand = cli.Command{
		Name:        "submit-lrp",
		Aliases:     []string{"sl"},
		Usage:       "Creates an app from JSON on lattice",
		Description: "ltc submit-lrp /path/to/json",
		Action:      factory.submitLrp,
	}

	return submitLrpCommand
}

func (factory *AppRunnerCommandFactory) MakeScaleAppCommand() cli.Command {
	var scaleFlags = []cli.Flag{
		cli.DurationFlag{
			Name:  "timeout, t",
			Usage: "Polling timeout for app to scale",
			Value: DefaultPollingTimeout,
		},
	}
	var scaleAppCommand = cli.Command{
		Name:        "scale",
		Aliases:     []string{"sc"},
		Usage:       "Scales an app on lattice",
		Description: "ltc scale APP_NAME NUM_INSTANCES",
		Action:      factory.scaleApp,
		Flags:       scaleFlags,
	}

	return scaleAppCommand
}

func (factory *AppRunnerCommandFactory) MakeUpdateRoutesCommand() cli.Command {
	var updateRoutesFlags = []cli.Flag{
		cli.BoolFlag{
			Name:  "no-routes",
			Usage: "Registers no routes for the app",
		},
	}
	var updateRoutesCommand = cli.Command{
		Name:        "update-routes",
		Aliases:     []string{"ur"},
		Usage:       "Updates the routes for a running app",
		Description: "ltc update-routes APP_NAME ROUTE,OTHER_ROUTE...",
		Action:      factory.updateAppRoutes,
		Flags:       updateRoutesFlags,
	}

	return updateRoutesCommand
}

func (factory *AppRunnerCommandFactory) MakeUpdateCommand() cli.Command {
	var updateFlags = []cli.Flag{
		cli.BoolFlag{
			Name:  "no-routes",
			Usage: "Registers no routes for the app",
		},
		cli.StringFlag{
			Name:  "http-routes, R",
			Usage: "Requests for HOST.SYSTEM_DOMAIN on port 80 will be forwarded to the associated container port. Container ports must be among those specified on create with --ports or with the EXPOSE Docker image directive. Replaces all existing routes. Usage: --http-routes HOST:CONTAINER_PORT[,...]",
		},
		cli.StringFlag{
			Name:  "tcp-routes, T",
			Usage: "Requests for the external port will be forwarded to the associated container port. Container ports must be among those specified on create with --ports or with the EXPOSE Docker image directive. Replaces all existing routes. Usage: EXTERNAL_PORT:CONTAINER_PORT[,...]",
		},
	}
	var updateCommand = cli.Command{
		Name:        "update",
		Aliases:     []string{"up"},
		Usage:       "Updates attributes of an existing application",
		Description: "ltc update APP_NAME [--http-routes HOST:CONTAINER_PORT[,...]] [--tcp-routes EXTERNAL_PORT:CONTAINER_PORT[,...]]\n",
		Action:      factory.updateApp,
		Flags:       updateFlags,
	}

	return updateCommand
}

func (factory *AppRunnerCommandFactory) MakeRemoveAppCommand() cli.Command {
	var removeAppCommand = cli.Command{
		Name:        "remove",
		Aliases:     []string{"rm"},
		Description: "ltc remove APP1_NAME [APP2_NAME APP3_NAME...]",
		Usage:       "Stops and removes app(s) from lattice",
		Action:      factory.removeApp,
	}

	return removeAppCommand
}

func (factory *AppRunnerCommandFactory) submitLrp(context *cli.Context) {
	filePath := context.Args().First()
	if filePath == "" {
		factory.UI.SayLine("Path to JSON is required")
		factory.ExitHandler.Exit(exit_codes.InvalidSyntax)
		return
	}

	jsonBytes, err := ioutil.ReadFile(filePath)
	if err != nil {
		factory.UI.SayLine(fmt.Sprintf("Error reading file: %s", err.Error()))
		factory.ExitHandler.Exit(exit_codes.FileSystemError)
		return
	}

	lrpName, err := factory.AppRunner.SubmitLrp(jsonBytes)
	if err != nil {
		factory.UI.SayLine(fmt.Sprintf("Error creating %s: %s", lrpName, err.Error()))
		factory.ExitHandler.Exit(exit_codes.CommandFailed)
		return
	}

	factory.UI.SayLine(colors.Green(fmt.Sprintf("Successfully submitted %s.", lrpName)))
	factory.UI.SayLine(fmt.Sprintf("To view the status of your application: ltc status %s", lrpName))
}

func (factory *AppRunnerCommandFactory) scaleApp(c *cli.Context) {
	appName := c.Args().First()
	instancesArg := c.Args().Get(1)
	timeoutFlag := c.Duration("timeout")
	if appName == "" || instancesArg == "" {
		factory.UI.SayIncorrectUsage("Please enter 'ltc scale APP_NAME NUMBER_OF_INSTANCES'")
		factory.ExitHandler.Exit(exit_codes.InvalidSyntax)
		return
	}

	instances, err := strconv.Atoi(instancesArg)
	if err != nil {
		factory.UI.SayIncorrectUsage("Number of Instances must be an integer")
		factory.ExitHandler.Exit(exit_codes.InvalidSyntax)
		return
	}

	factory.setAppInstances(timeoutFlag, appName, instances)
}

func (factory *AppRunnerCommandFactory) updateApp(c *cli.Context) {
	appName := c.Args().First()
	if appName == "" {
		factory.UI.SayIncorrectUsage("Please enter 'ltc update APP_NAME' followed by at least one of: '--no-routes', '--http-routes' or '--tcp-routes' flag.")
		factory.ExitHandler.Exit(exit_codes.InvalidSyntax)
		return
	}

	httpRoutesFlag := c.String("http-routes")
	tcpRoutesFlag := c.String("tcp-routes")
	noRoutes := c.Bool("no-routes")

	if httpRoutesFlag == "" && tcpRoutesFlag == "" && !noRoutes {
		factory.UI.SayIncorrectUsage("Please enter 'ltc update APP_NAME' followed by at least one of: '--no-routes', '--http-routes' or '--tcp-routes' flag.")
		factory.ExitHandler.Exit(exit_codes.InvalidSyntax)
		return
	}

	updateAppParams := app_runner.UpdateAppParams{}
	updateAppParams.Name = appName
	updateAppParams.NoRoutes = noRoutes

	routeOverrides, err := factory.ParseRouteOverrides(httpRoutesFlag)
	if err != nil {
		factory.UI.SayLine(err.Error())
		factory.ExitHandler.Exit(exit_codes.InvalidSyntax)
		return
	}
	updateAppParams.RouteOverrides = routeOverrides

	tcpRoutes, err := factory.ParseTcpRoutes(tcpRoutesFlag)
	if err != nil {
		factory.UI.SayLine(err.Error())
		factory.ExitHandler.Exit(exit_codes.InvalidSyntax)
		return
	}
	updateAppParams.TcpRoutes = tcpRoutes

	if err := factory.AppRunner.UpdateApp(updateAppParams); err != nil {
		factory.UI.SayLine(fmt.Sprintf("Error updating application: %s", err))
		factory.ExitHandler.Exit(exit_codes.CommandFailed)
		return
	}

	factory.UI.SayLine(fmt.Sprintf("Updating %s routes. You can check this app's current routes by running 'ltc status %s'", appName, appName))
}

func (factory *AppRunnerCommandFactory) updateAppRoutes(c *cli.Context) {
	appName := c.Args().First()
	userDefinedRoutes := c.Args().Get(1)
	noRoutesFlag := c.Bool("no-routes")

	if appName == "" || (userDefinedRoutes == "" && !noRoutesFlag) {
		factory.UI.SayIncorrectUsage("Please enter 'ltc update-routes APP_NAME NEW_ROUTES' or pass '--no-routes' flag.")
		factory.ExitHandler.Exit(exit_codes.InvalidSyntax)
		return
	}

	desiredRoutes := app_runner.RouteOverrides{}
	var err error
	if !noRoutesFlag {
		desiredRoutes, err = factory.ParseRouteOverrides(userDefinedRoutes)
		if err != nil {
			factory.UI.SayLine(err.Error())
			factory.ExitHandler.Exit(exit_codes.InvalidSyntax)
			return
		}
	}

	if err := factory.AppRunner.UpdateAppRoutes(appName, desiredRoutes); err != nil {
		factory.UI.SayLine(fmt.Sprintf("Error updating routes: %s", err))
		factory.ExitHandler.Exit(exit_codes.CommandFailed)
		return
	}

	factory.UI.SayLine(fmt.Sprintf("Updating %s routes. You can check this app's current routes by running 'ltc status %s'", appName, appName))
}

func (factory *AppRunnerCommandFactory) setAppInstances(pollTimeout time.Duration, appName string, instances int) {
	if err := factory.AppRunner.ScaleApp(appName, instances); err != nil {
		factory.UI.SayLine(fmt.Sprintf("Error Scaling App to %d instances: %s", instances, err))
		factory.ExitHandler.Exit(exit_codes.CommandFailed)
		return
	}

	factory.UI.SayLine(fmt.Sprintf("Scaling %s to %d instances", appName, instances))

	if ok := factory.pollUntilAllInstancesRunning(pollTimeout, appName, instances, "scale"); ok {
		factory.UI.SayLine(colors.Green("App Scaled Successfully"))
	}
}

func (factory *AppRunnerCommandFactory) removeApp(c *cli.Context) {
	appNames := c.Args()
	if len(appNames) == 0 {
		factory.UI.SayIncorrectUsage("App Name required")
		factory.ExitHandler.Exit(exit_codes.InvalidSyntax)
		return
	}

	for _, appName := range appNames {
		factory.UI.SayLine(fmt.Sprintf("Removing %s...", appName))
		if err := factory.AppRunner.RemoveApp(appName); err != nil {
			factory.UI.SayLine(fmt.Sprintf("Error stopping %s: %s", appName, err))
			factory.ExitHandler.Exit(exit_codes.CommandFailed) // TODO: handle partial failure
		}
	}
}

func (factory *AppRunnerCommandFactory) WaitForAppCreation(appName string, pollTimeout time.Duration, instanceCount int, noRoutesFlag bool, routeOverrides app_runner.RouteOverrides, tcpRoutes app_runner.TcpRoutes, monitorPort uint16, exposedPorts []uint16) {
	factory.UI.SayLine("Creating App: " + appName)

	go factory.TailedLogsOutputter.OutputTailedLogs(appName)
	defer factory.TailedLogsOutputter.StopOutputting()

	ok := factory.pollUntilAllInstancesRunning(pollTimeout, appName, instanceCount, "start")
	if noRoutesFlag {
		factory.UI.SayLine(colors.Green(appName + " is now running."))
		return
	} else if ok {
		factory.UI.SayLine(colors.Green(appName + " is now running."))
		factory.UI.SayLine("App is reachable at:")
	} else {
		factory.UI.SayLine("App will be reachable at:")
	}
	if tcpRoutes != nil {
		for _, tcpRoute := range tcpRoutes {
			factory.UI.SayLine(colors.Green(factory.externalPortMappingForApp(tcpRoute.ExternalPort, tcpRoute.Port)))
		}
	}
	if routeOverrides != nil {
		for _, route := range routeOverrides {
			factory.UI.SayLine(colors.Green(factory.urlForAppName(route.HostnamePrefix)))
		}
	} else if len(tcpRoutes) == 0 {
		factory.displayDefaultRoutes(appName, monitorPort, exposedPorts)
	}
}

func (factory *AppRunnerCommandFactory) displayDefaultRoutes(appName string, monitorPort uint16, exposedPorts []uint16) {
	factory.UI.SayLine(colors.Green(factory.urlForAppName(appName)))

	primaryPort := route_helpers.GetPrimaryPort(monitorPort, exposedPorts)
	appRoutes := route_helpers.BuildDefaultRoutingInfo(appName, exposedPorts, primaryPort, factory.Domain)
	for _, appRoute := range appRoutes {
		factory.UI.SayLine(colors.Green(factory.urlForAppNameAndPort(appName, appRoute.Port)))
	}
}

func (factory *AppRunnerCommandFactory) externalPortMappingForApp(externalPort uint16, containerPort uint16) string {
	return fmt.Sprintf("%s:%d", factory.Domain, externalPort)
}

func (factory *AppRunnerCommandFactory) urlForAppNameAndPort(name string, port uint16) string {
	return fmt.Sprintf("http://%s-%d.%s", name, port, factory.Domain)
}

func (factory *AppRunnerCommandFactory) urlForAppName(name string) string {
	return fmt.Sprintf("http://%s.%s", name, factory.Domain)
}

func (factory *AppRunnerCommandFactory) pollUntilAllInstancesRunning(pollTimeout time.Duration, appName string, instances int, action pollingAction) bool {
	var placementErrorOccurred bool
	ok := factory.pollUntilSuccess(pollTimeout, func() bool {
		numberOfRunningInstances, placementError, _ := factory.AppExaminer.RunningAppInstancesInfo(appName)
		if placementError {
			factory.UI.SayLine(colors.Red("Error, could not place all instances: insufficient resources. Try requesting fewer instances or reducing the requested memory or disk capacity."))
			placementErrorOccurred = true
			return true
		}
		return numberOfRunningInstances == instances
	}, true)

	if placementErrorOccurred {
		factory.ExitHandler.Exit(exit_codes.PlacementError)
		return false
	}
	if !ok {
		if action == pollingStart {
			factory.UI.SayLine(colors.Red("Timed out waiting for the container to come up."))
			factory.UI.SayLine("This typically happens because docker layers can take time to download.")
			factory.UI.SayLine("Lattice is still downloading your application in the background.")
		} else {
			factory.UI.SayLine(colors.Red("Timed out waiting for the container to scale."))
			factory.UI.SayLine("Lattice is still scaling your application in the background.")
		}
		factory.UI.SayLine(fmt.Sprintf("To view logs:\n\tltc logs %s", appName))
		factory.UI.SayLine(fmt.Sprintf("To view status:\n\tltc status %s", appName))
		factory.UI.SayNewLine()
	}
	return ok
}

func (factory *AppRunnerCommandFactory) pollUntilSuccess(pollTimeout time.Duration, pollingFunc func() bool, outputProgress bool) (ok bool) {
	startingTime := factory.Clock.Now()
	for startingTime.Add(pollTimeout).After(factory.Clock.Now()) {
		if result := pollingFunc(); result {
			factory.UI.SayNewLine()
			return true
		}
		if outputProgress {
			factory.UI.Say(".")
		}

		factory.Clock.Sleep(1 * time.Second)
	}
	factory.UI.SayNewLine()
	return false
}

func (factory *AppRunnerCommandFactory) BuildAppEnvironment(envVars []string, appName string) map[string]string {
	environment := factory.BuildEnvironment(envVars)
	if _, found := environment["PROCESS_GUID"]; !found {
		environment["PROCESS_GUID"] = appName
	}
	return environment
}

func (factory *AppRunnerCommandFactory) BuildEnvironment(envVars []string) map[string]string {
	environment := make(map[string]string)
	for _, envVarPair := range envVars {
		name, value := parseEnvVarPair(envVarPair)
		if value == "" {
			value = factory.grabVarFromEnv(name)
		}

		environment[name] = value
	}
	return environment
}

func (factory *AppRunnerCommandFactory) grabVarFromEnv(name string) string {
	for _, envVarPair := range factory.Env {
		if k := strings.SplitN(envVarPair, "=", 2)[0]; k == name {
			_, value := parseEnvVarPair(envVarPair)
			return value
		}
	}
	return ""
}

func (factory *AppRunnerCommandFactory) ParseTcpRoutes(tcpRoutesFlag string) (app_runner.TcpRoutes, error) {
	var tcpRoutes app_runner.TcpRoutes

	if tcpRoutesFlag == "" {
		return tcpRoutes, nil
	}

	for _, tcpRoute := range strings.Split(tcpRoutesFlag, ",") {
		if tcpRoute == "" {
			continue
		}

		portsArr := strings.Split(tcpRoute, ":")
		if len(portsArr) < 2 {
			return nil, errors.New(MalformedTcpRouteErrorMessage)
		}

		externalPort, err := getPort(portsArr[0])
		if err != nil {
			return nil, err
		}

		containerPort, err := getPort(portsArr[1])
		if err != nil {
			return nil, err
		}

		for _, reservedPort := range reservedPorts {
			if externalPort == reservedPort {
				return nil, errors.New(fmt.Sprintf(ReservedPortErrorMessage, externalPort))
			}
		}

		tcpRoutes = append(tcpRoutes, app_runner.TcpRoute{ExternalPort: externalPort, Port: containerPort})
	}

	return tcpRoutes, nil
}

func getPort(port string) (uint16, error) {
	mayBePort, err := strconv.Atoi(port)
	if err != nil {
		return 0, errors.New(InvalidPortErrorMessage)
	}

	if mayBePort <= 0 || mayBePort > 65535 {
		return 0, errors.New(InvalidPortErrorMessage)
	}

	return uint16(mayBePort), nil
}

func (factory *AppRunnerCommandFactory) ParseRouteOverrides(routes string) (app_runner.RouteOverrides, error) {
	var routeOverrides app_runner.RouteOverrides

	for _, route := range strings.Split(routes, ",") {
		if route == "" {
			continue
		}

		routeArr := strings.Split(route, ":")
		if len(routeArr) < 2 {
			return nil, errors.New(MalformedRouteErrorMessage)
		}

		hostnamePrefix := strings.Trim(routeArr[0], " ")
		if len(hostnamePrefix) == 0 {
			return nil, errors.New(MalformedRouteErrorMessage)
		}

		port, err := getPort(routeArr[1])
		if err != nil {
			return nil, err
		}

		routeOverrides = append(routeOverrides, app_runner.RouteOverride{HostnamePrefix: hostnamePrefix, Port: port})
	}

	return routeOverrides, nil
}

func parseEnvVarPair(envVarPair string) (name, value string) {
	s := strings.SplitN(envVarPair, "=", 2)
	if len(s) > 1 {
		return s[0], s[1]
	}
	return s[0], ""
}

func (factory *AppRunnerCommandFactory) GetMonitorConfig(exposedPorts []uint16, portMonitorFlag int, noMonitorFlag bool, urlMonitorFlag, monitorCommandFlag string, monitorTimeoutFlag time.Duration) (app_runner.MonitorConfig, error) {
	if noMonitorFlag {
		return app_runner.MonitorConfig{Method: app_runner.NoMonitor}, nil
	}

	if monitorCommandFlag != "" {
		return app_runner.MonitorConfig{
			Method:        app_runner.CustomMonitor,
			CustomCommand: monitorCommandFlag,
		}, nil
	}

	if urlMonitorFlag != "" {
		urlMonitorArr := strings.Split(urlMonitorFlag, ":")
		if len(urlMonitorArr) != 2 {
			return app_runner.MonitorConfig{}, errors.New(InvalidPortErrorMessage)
		}

		urlMonitorPort, err := strconv.Atoi(urlMonitorArr[0])
		if err != nil {
			return app_runner.MonitorConfig{}, errors.New(InvalidPortErrorMessage)
		}

		if err := checkPortExposed(exposedPorts, uint16(urlMonitorPort)); err != nil {
			return app_runner.MonitorConfig{}, err
		}

		return app_runner.MonitorConfig{
			Method:  app_runner.URLMonitor,
			Port:    uint16(urlMonitorPort),
			URI:     urlMonitorArr[1],
			Timeout: monitorTimeoutFlag,
		}, nil
	}

	var sortedPorts []int
	for _, port := range exposedPorts {
		sortedPorts = append(sortedPorts, int(port))
	}
	sort.Ints(sortedPorts)

	// Unsafe array access:  because we'll default exposing 8080 if
	// both --ports is empty and docker image has no EXPOSE ports
	monitorPort := uint16(sortedPorts[0])
	if portMonitorFlag > 0 {
		monitorPort = uint16(portMonitorFlag)
	}

	if err := checkPortExposed(exposedPorts, monitorPort); err != nil {
		return app_runner.MonitorConfig{}, err
	}

	return app_runner.MonitorConfig{
		Method:  app_runner.PortMonitor,
		Port:    uint16(monitorPort),
		Timeout: monitorTimeoutFlag,
	}, nil
}

func checkPortExposed(exposedPorts []uint16, portToCheck uint16) error {
	for _, port := range exposedPorts {
		if port == uint16(portToCheck) {
			return nil
		}
	}

	return errors.New(MonitorPortNotExposed)
}
