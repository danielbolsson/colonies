package server

import (
	"errors"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/colonyos/colonies/pkg/cluster"
	"github.com/colonyos/colonies/pkg/core"
	cronlib "github.com/colonyos/colonies/pkg/cron"
	"github.com/colonyos/colonies/pkg/database"
	"github.com/colonyos/colonies/pkg/planner"
	"github.com/colonyos/colonies/pkg/planner/basic"
	log "github.com/sirupsen/logrus"
)

const TIMEOUT_RELEASE_INTERVALL = 1
const TIMEOUT_GENERATOR_TRIGGER_INTERVALL = 1
const TIMEOUT_CRON_TRIGGER_INTERVALL = 1

type command struct {
	stop                   bool
	errorChan              chan error
	process                *core.Process
	count                  int
	colony                 *core.Colony
	colonyID               string
	colonyReplyChan        chan *core.Colony
	coloniesReplyChan      chan []*core.Colony
	processReplyChan       chan *core.Process
	processesReplyChan     chan []*core.Process
	processGraphReplyChan  chan *core.ProcessGraph
	processGraphsReplyChan chan []*core.ProcessGraph
	statisticsReplyChan    chan *core.Statistics
	runtimeReplyChan       chan *core.Runtime
	runtimesReplyChan      chan []*core.Runtime
	attributeReplyChan     chan core.Attribute
	generatorReplyChan     chan *core.Generator
	generatorsReplyChan    chan []*core.Generator
	cronReplyChan          chan *core.Cron
	cronsReplyChan         chan []*core.Cron
	handler                func(cmd *command)
}

type coloniesController struct {
	db            database.Database
	cmdQueue      chan *command
	planner       planner.Planner
	wsSubCtrl     *wsSubscriptionController
	relayServer   *cluster.RelayServer
	eventHandler  *eventHandler
	stopFlag      bool
	stopMutex     sync.Mutex
	leaderMutex   sync.Mutex
	thisNode      cluster.Node
	clusterConfig cluster.Config
	etcdServer    *cluster.EtcdServer
	leader        bool
}

func createColoniesController(db database.Database, thisNode cluster.Node, clusterConfig cluster.Config, etcdDataPath string) *coloniesController {
	controller := &coloniesController{}
	controller.db = db
	controller.thisNode = thisNode
	controller.clusterConfig = clusterConfig
	controller.etcdServer = cluster.CreateEtcdServer(controller.thisNode, controller.clusterConfig, etcdDataPath)
	controller.etcdServer.Start()
	controller.etcdServer.WaitToStart()
	controller.leader = false

	controller.relayServer = cluster.CreateRelayServer(controller.thisNode, controller.clusterConfig)
	controller.eventHandler = createEventHandler(controller.relayServer)
	controller.wsSubCtrl = createWSSubscriptionController(controller.eventHandler)
	controller.planner = basic.CreatePlanner()

	controller.cmdQueue = make(chan *command)

	controller.tryBecomeLeader()
	go controller.masterWorker()
	go controller.timeoutLoop()
	go controller.generatorTriggerLoop()
	go controller.cronTriggerLoop()

	return controller
}

func (controller *coloniesController) submitWorkflow(generator *core.Generator) {
	workflowSpec, err := core.ConvertJSONToWorkflowSpec(generator.WorkflowSpec)
	if err != nil {
		log.WithFields(log.Fields{"Error": err}).Error("Failed to parse workflow spec")
		return
	}

	generatorArgs, err := controller.db.GetGeneratorArgs(generator.ID, generator.Trigger)
	var args []string
	for _, generatorArg := range generatorArgs {
		args = append(args, generatorArg.Arg)
	}

	log.WithFields(log.Fields{
		"GeneratorId": generator.ID,
		"Trigger":     generator.Trigger,
		"Args":        args}).
		Debug("Generator submitting workflow")

	_, err = controller.createProcessGraph(workflowSpec, args)
	if err != nil {
		log.WithFields(log.Fields{
			"Error": err}).
			Error("Failed to create processgraph")
		return
	}

	// Now it safe to remove the args since they are now attached to a process graph
	for _, generatorArg := range generatorArgs {
		controller.db.DeleteGeneratorArgByID(generatorArg.ID)
	}

	err = controller.db.SetGeneratorLastRun(generator.ID)
	if err != nil {
		log.WithFields(log.Fields{"Error": err}).Error("Failed mark generator as run")
	}
}

func (controller *coloniesController) triggerGenerators() {
	cmd := &command{handler: func(cmd *command) {
		generatorsFromDB, err := controller.db.FindAllGenerators()
		if err != nil {
			log.WithFields(log.Fields{"Error": err}).Error("Failed get all generators from db")
			return
		}
		for _, generator := range generatorsFromDB {
			counter, err := controller.db.CountGeneratorArgs(generator.ID)
			if err != nil {
				log.WithFields(log.Fields{"Error": err}).Error("Failed count generator args from db")
				continue
			}
			if counter >= generator.Trigger {
				timesToSubmit := counter / generator.Trigger
				for i := 0; i < timesToSubmit; i++ {
					log.WithFields(log.Fields{
						"GeneratorId": generator.ID,
						"Counter":     counter}).
						Info("Generator threshold reached, submitting workflow")
					controller.submitWorkflow(generator)
				}
			}
		}
	}}

	controller.cmdQueue <- cmd
}

func (controller *coloniesController) calcNextRun(cron *core.Cron) time.Time {
	nextRun := time.Time{}
	var err error
	if cron.Interval > 0 {
		nextRun, err = cronlib.NextIntervall(cron.Interval)
		if err != nil {
			log.WithFields(log.Fields{"Error": err}).Error("Failed generate random next run")
		}
	} else if cron.Interval > 0 && cron.Random {
		nextRun, err = cronlib.Random(cron.Interval)
		if err != nil {
			log.WithFields(log.Fields{"Error": err}).Error("Failed generate random next run")
		}
	} else {
		nextRun, err = cronlib.Next(cron.CronExpression)
		if err != nil {
			log.WithFields(log.Fields{"Error": err}).Error("Failed generate next run based on cron expression")
		}
	}

	return nextRun
}

func (controller *coloniesController) startCron(cron *core.Cron) {
	workflowSpec, err := core.ConvertJSONToWorkflowSpec(cron.WorkflowSpec)
	if err != nil {
		log.WithFields(log.Fields{"Error": err}).Error("Failed to parsing WorkflowSpec")
	}
	processGraph, err := controller.createProcessGraph(workflowSpec, []string{})
	if err != nil {
		log.WithFields(log.Fields{"Error": err}).Error("Failed to parse workflow spec")
	}

	nextRun := controller.calcNextRun(cron)
	controller.db.UpdateCron(cron.ID, nextRun, time.Now(), processGraph.ID)
}

func (controller *coloniesController) triggerCrons() {
	cmd := &command{handler: func(cmd *command) {
		crons, err := controller.db.FindAllCrons()
		if err != nil {
			log.WithFields(log.Fields{"Error": err}).Error("Failed getting all crons")
			return
		}
		for _, cron := range crons {
			t := time.Time{}
			if t.Unix() == cron.NextRun.Unix() { // This if-statement will be true the first time the cron is evaluted
				nextRun := controller.calcNextRun(cron)
				controller.db.UpdateCron(cron.ID, nextRun, time.Time{}, "")
				cron.NextRun = nextRun
			}
			if cron.HasExpired() {
				log.WithFields(log.Fields{"CronId": cron.ID}).Info("Triggering cron workflow")
				controller.startCron(cron)
			}
		}
	}}

	controller.cmdQueue <- cmd
}

func (controller *coloniesController) addGenerator(generator *core.Generator) (*core.Generator, error) {
	cmd := &command{generatorReplyChan: make(chan *core.Generator, 1),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			err := controller.db.AddGenerator(generator)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			addedGenerator, err := controller.db.GetGeneratorByID(generator.ID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			cmd.generatorReplyChan <- addedGenerator
		}}

	controller.cmdQueue <- cmd
	select {
	case err := <-cmd.errorChan:
		return nil, err
	case addedGenerator := <-cmd.generatorReplyChan:
		return addedGenerator, nil
	}
}

func (controller *coloniesController) getGenerator(generatorID string) (*core.Generator, error) {
	cmd := &command{generatorReplyChan: make(chan *core.Generator, 1),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			generator, err := controller.db.GetGeneratorByID(generatorID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			cmd.generatorReplyChan <- generator
		}}

	controller.cmdQueue <- cmd
	select {
	case err := <-cmd.errorChan:
		return nil, err
	case generator := <-cmd.generatorReplyChan:
		return generator, nil
	}
}

func (controller *coloniesController) getGenerators(colonyID string, count int) ([]*core.Generator, error) {
	cmd := &command{generatorsReplyChan: make(chan []*core.Generator, 1),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			generators, err := controller.db.FindGeneratorsByColonyID(colonyID, count)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			cmd.generatorsReplyChan <- generators
		}}

	controller.cmdQueue <- cmd
	select {
	case err := <-cmd.errorChan:
		return nil, err
	case generators := <-cmd.generatorsReplyChan:
		return generators, nil
	}
}

func (controller *coloniesController) packGenerator(generatorID string, colonyID, arg string) error {
	cmd := &command{errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			generatorArg := core.CreateGeneratorArg(generatorID, colonyID, arg)
			cmd.errorChan <- controller.db.AddGeneratorArg(generatorArg)
		}}

	controller.cmdQueue <- cmd
	select {
	case err := <-cmd.errorChan:
		return err
	}
}

func (controller *coloniesController) deleteGenerator(generatorID string) error {
	cmd := &command{errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			cmd.errorChan <- controller.db.DeleteGeneratorByID(generatorID)
		}}

	controller.cmdQueue <- cmd
	return <-cmd.errorChan
}

func (controller *coloniesController) addCron(cron *core.Cron) (*core.Cron, error) {
	cmd := &command{cronReplyChan: make(chan *core.Cron, 1),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			err := controller.db.AddCron(cron)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			addedCron, err := controller.db.GetCronByID(cron.ID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			cmd.cronReplyChan <- addedCron
		}}

	controller.cmdQueue <- cmd
	select {
	case err := <-cmd.errorChan:
		return nil, err
	case addedCron := <-cmd.cronReplyChan:
		return addedCron, nil
	}
}

func (controller *coloniesController) getCron(cronID string) (*core.Cron, error) {
	cmd := &command{cronReplyChan: make(chan *core.Cron, 1),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			cron, err := controller.db.GetCronByID(cronID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			cmd.cronReplyChan <- cron
		}}

	controller.cmdQueue <- cmd
	select {
	case err := <-cmd.errorChan:
		return nil, err
	case cron := <-cmd.cronReplyChan:
		return cron, nil
	}
}

func (controller *coloniesController) getCrons(colonyID string, count int) ([]*core.Cron, error) {
	cmd := &command{cronsReplyChan: make(chan []*core.Cron, 1),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			crons, err := controller.db.FindCronsByColonyID(colonyID, count)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			cmd.cronsReplyChan <- crons
		}}

	controller.cmdQueue <- cmd
	select {
	case err := <-cmd.errorChan:
		return nil, err
	case crons := <-cmd.cronsReplyChan:
		return crons, nil
	}
}

func (controller *coloniesController) runCron(cronID string) (*core.Cron, error) {
	cmd := &command{cronReplyChan: make(chan *core.Cron, 1),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			cron, err := controller.db.GetCronByID(cronID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			controller.startCron(cron)
			cmd.cronReplyChan <- cron
		}}

	controller.cmdQueue <- cmd
	select {
	case err := <-cmd.errorChan:
		return nil, err
	case cron := <-cmd.cronReplyChan:
		return cron, nil
	}
}

func (controller *coloniesController) deleteCron(cronID string) error {
	cmd := &command{errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			err := controller.db.DeleteCronByID(cronID)
			cmd.errorChan <- err
		}}

	controller.cmdQueue <- cmd
	return <-cmd.errorChan
}

func (controller *coloniesController) subscribeProcesses(runtimeID string, subscription *subscription) error {
	cmd := &command{errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			controller.wsSubCtrl.addProcessesSubscriber(runtimeID, subscription)
			cmd.errorChan <- nil
		}}
	controller.cmdQueue <- cmd

	return <-cmd.errorChan
}

func (controller *coloniesController) subscribeProcess(runtimeID string, subscription *subscription) error {
	cmd := &command{errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			process, err := controller.db.GetProcessByID(subscription.processID)
			if err != nil {
				cmd.errorChan <- err
			}

			controller.wsSubCtrl.addProcessSubscriber(runtimeID, process, subscription)
			cmd.errorChan <- nil
		}}
	controller.cmdQueue <- cmd

	return <-cmd.errorChan
}

func (controller *coloniesController) getColonies() ([]*core.Colony, error) {
	cmd := &command{coloniesReplyChan: make(chan []*core.Colony),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			colonies, err := controller.db.GetColonies()
			if err != nil {
				cmd.errorChan <- err
				return
			}
			cmd.coloniesReplyChan <- colonies
		}}

	controller.cmdQueue <- cmd
	var colonies []*core.Colony
	select {
	case err := <-cmd.errorChan:
		return colonies, err
	case colonies := <-cmd.coloniesReplyChan:
		return colonies, nil
	}
}

func (controller *coloniesController) getColony(colonyID string) (*core.Colony, error) {
	cmd := &command{colonyReplyChan: make(chan *core.Colony),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			colony, err := controller.db.GetColonyByID(colonyID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			cmd.colonyReplyChan <- colony
		}}

	controller.cmdQueue <- cmd
	select {
	case err := <-cmd.errorChan:
		return nil, err
	case colony := <-cmd.colonyReplyChan:
		return colony, nil
	}
}

func (controller *coloniesController) addColony(colony *core.Colony) (*core.Colony, error) {
	cmd := &command{colonyReplyChan: make(chan *core.Colony, 1),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			err := controller.db.AddColony(colony)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			addedColony, err := controller.db.GetColonyByID(colony.ID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			cmd.colonyReplyChan <- addedColony
		}}

	controller.cmdQueue <- cmd
	select {
	case err := <-cmd.errorChan:
		return nil, err
	case addedColony := <-cmd.colonyReplyChan:
		return addedColony, nil
	}
}

func (controller *coloniesController) deleteColony(colonyID string) error {
	cmd := &command{errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			err := controller.db.DeleteColonyByID(colonyID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			cmd.errorChan <- nil
		}}

	controller.cmdQueue <- cmd
	return <-cmd.errorChan
}

func (controller *coloniesController) addRuntime(runtime *core.Runtime) (*core.Runtime, error) {
	cmd := &command{runtimeReplyChan: make(chan *core.Runtime, 1),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			err := controller.db.AddRuntime(runtime)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			addedRuntime, err := controller.db.GetRuntimeByID(runtime.ID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			cmd.runtimeReplyChan <- addedRuntime
		}}

	controller.cmdQueue <- cmd
	select {
	case err := <-cmd.errorChan:
		return nil, err
	case addedRuntime := <-cmd.runtimeReplyChan:
		return addedRuntime, nil
	}
}

func (controller *coloniesController) getRuntime(runtimeID string) (*core.Runtime, error) {
	cmd := &command{runtimeReplyChan: make(chan *core.Runtime),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			runtime, err := controller.db.GetRuntimeByID(runtimeID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			cmd.runtimeReplyChan <- runtime
		}}

	controller.cmdQueue <- cmd
	select {
	case err := <-cmd.errorChan:
		return nil, err
	case runtime := <-cmd.runtimeReplyChan:
		return runtime, nil
	}
}

func (controller *coloniesController) getRuntimeByColonyID(colonyID string) ([]*core.Runtime, error) {
	cmd := &command{runtimesReplyChan: make(chan []*core.Runtime),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			runtimes, err := controller.db.GetRuntimesByColonyID(colonyID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			cmd.runtimesReplyChan <- runtimes
		}}

	controller.cmdQueue <- cmd
	var runtimes []*core.Runtime
	select {
	case err := <-cmd.errorChan:
		return runtimes, err
	case runtimes := <-cmd.runtimesReplyChan:
		return runtimes, nil
	}

	return runtimes, nil
}

func (controller *coloniesController) approveRuntime(runtimeID string) error {
	cmd := &command{errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			runtime, err := controller.db.GetRuntimeByID(runtimeID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			cmd.errorChan <- controller.db.ApproveRuntime(runtime)
		}}

	controller.cmdQueue <- cmd
	return <-cmd.errorChan
}

func (controller *coloniesController) rejectRuntime(runtimeID string) error {
	cmd := &command{errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			runtime, err := controller.db.GetRuntimeByID(runtimeID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			cmd.errorChan <- controller.db.RejectRuntime(runtime)
		}}

	controller.cmdQueue <- cmd
	return <-cmd.errorChan
}

func (controller *coloniesController) deleteRuntime(runtimeID string) error {
	cmd := &command{errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			err := controller.db.DeleteRuntimeByID(runtimeID)
			cmd.errorChan <- err
		}}

	controller.cmdQueue <- cmd
	return <-cmd.errorChan
}

func (controller *coloniesController) addProcessAndSetWaitingDeadline(process *core.Process) (*core.Process, error) {
	err := controller.db.AddProcess(process)
	if err != nil {
		return nil, err
	}

	maxWaitTime := process.ProcessSpec.MaxWaitTime
	if maxWaitTime > 0 {
		err := controller.db.SetWaitDeadline(process, time.Now().Add(time.Duration(maxWaitTime)*time.Second))
		if err != nil {
			return nil, err
		}
	}

	addedProcess, err := controller.db.GetProcessByID(process.ID)
	if err != nil {
		return nil, err
	}

	return addedProcess, nil
}

func (controller *coloniesController) addProcess(process *core.Process) (*core.Process, error) {
	cmd := &command{processReplyChan: make(chan *core.Process, 1),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			addedProcess, err := controller.addProcessAndSetWaitingDeadline(process)
			if err != nil {
				cmd.errorChan <- err
				return
			}

			controller.eventHandler.signal(addedProcess)
			cmd.processReplyChan <- addedProcess
		}}

	controller.cmdQueue <- cmd
	select {
	case err := <-cmd.errorChan:
		return nil, err
	case process := <-cmd.processReplyChan:
		return process, nil
	}
}

func (controller *coloniesController) getProcess(processID string) (*core.Process, error) {
	cmd := &command{processReplyChan: make(chan *core.Process, 1),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			process, err := controller.db.GetProcessByID(processID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			cmd.processReplyChan <- process
		}}

	controller.cmdQueue <- cmd
	select {
	case err := <-cmd.errorChan:
		return nil, err
	case process := <-cmd.processReplyChan:
		return process, nil
	}
}

func (controller *coloniesController) findProcessHistory(colonyID string, runtimeID string, seconds int, state int) ([]*core.Process, error) {
	cmd := &command{processesReplyChan: make(chan []*core.Process),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			var processes []*core.Process
			var err error
			if runtimeID == "" {
				processes, err = controller.db.FindProcessesByColonyID(colonyID, seconds, state)
				if err != nil {
					cmd.errorChan <- err
					return
				}
			} else {
				processes, err = controller.db.FindProcessesByRuntimeID(colonyID, runtimeID, seconds, state)
				if err != nil {
					cmd.errorChan <- err
					return
				}
			}
			cmd.processesReplyChan <- processes
		}}

	controller.cmdQueue <- cmd
	var processes []*core.Process
	select {
	case err := <-cmd.errorChan:
		return processes, err
	case processes := <-cmd.processesReplyChan:
		return processes, nil
	}
}

func (controller *coloniesController) findPrioritizedProcesses(runtimeID string, colonyID string, count int) ([]*core.Process, error) {
	cmd := &command{processesReplyChan: make(chan []*core.Process),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			var processes []*core.Process
			if count > MAX_COUNT {
				cmd.errorChan <- errors.New("Count is larger than MaxCount limit <" + strconv.Itoa(MAX_COUNT) + ">")
				return
			}
			processes, err := controller.db.FindWaitingProcesses(colonyID, count)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			prioritizedProcesses := controller.planner.Prioritize(runtimeID, processes, count, false)
			cmd.processesReplyChan <- prioritizedProcesses
		}}

	controller.cmdQueue <- cmd
	var processes []*core.Process
	select {
	case err := <-cmd.errorChan:
		return processes, err
	case processes := <-cmd.processesReplyChan:
		return processes, nil
	}
}

func (controller *coloniesController) findWaitingProcesses(colonyID string, count int) ([]*core.Process, error) {
	cmd := &command{processesReplyChan: make(chan []*core.Process),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			if count > MAX_COUNT {
				cmd.errorChan <- errors.New("Count is larger than MaxCount limit <" + strconv.Itoa(MAX_COUNT) + ">")
				return
			}
			processes, err := controller.db.FindWaitingProcesses(colonyID, count)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			cmd.processesReplyChan <- processes
		}}

	controller.cmdQueue <- cmd
	var processes []*core.Process
	select {
	case err := <-cmd.errorChan:
		return processes, err
	case processes := <-cmd.processesReplyChan:
		return processes, nil
	}
}

func (controller *coloniesController) findRunningProcesses(colonyID string, count int) ([]*core.Process, error) {
	cmd := &command{processesReplyChan: make(chan []*core.Process),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			if count > MAX_COUNT {
				cmd.errorChan <- errors.New("Count is larger than MaxCount limit <" + strconv.Itoa(MAX_COUNT) + ">")
				return
			}
			processes, err := controller.db.FindRunningProcesses(colonyID, count)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			cmd.processesReplyChan <- processes
		}}

	controller.cmdQueue <- cmd
	var processes []*core.Process
	select {
	case err := <-cmd.errorChan:
		return processes, err
	case processes := <-cmd.processesReplyChan:
		return processes, nil
	}
}

func (controller *coloniesController) findSuccessfulProcesses(colonyID string, count int) ([]*core.Process, error) {
	cmd := &command{processesReplyChan: make(chan []*core.Process),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			if count > MAX_COUNT {
				cmd.errorChan <- errors.New("Count is larger than MaxCount limit <" + strconv.Itoa(MAX_COUNT) + ">")
				return
			}
			processes, err := controller.db.FindSuccessfulProcesses(colonyID, count)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			cmd.processesReplyChan <- processes
		}}

	controller.cmdQueue <- cmd
	var processes []*core.Process
	select {
	case err := <-cmd.errorChan:
		return processes, err
	case processes := <-cmd.processesReplyChan:
		return processes, nil
	}
}

func (controller *coloniesController) findFailedProcesses(colonyID string, count int) ([]*core.Process, error) {
	cmd := &command{processesReplyChan: make(chan []*core.Process),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			if count > MAX_COUNT {
				cmd.errorChan <- errors.New("Count is larger than MaxCount limit <" + strconv.Itoa(MAX_COUNT) + ">")
				return
			}
			processes, err := controller.db.FindFailedProcesses(colonyID, count)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			cmd.processesReplyChan <- processes
		}}

	controller.cmdQueue <- cmd
	var processes []*core.Process
	select {
	case err := <-cmd.errorChan:
		return processes, err
	case processes := <-cmd.processesReplyChan:
		return processes, nil
	}
}

func (controller *coloniesController) updateProcessGraph(graph *core.ProcessGraph) error {
	graph.SetStorage(controller.db)
	return graph.UpdateProcessIDs()
}

func (controller *coloniesController) createProcessGraph(workflowSpec *core.WorkflowSpec, args []string) (*core.ProcessGraph, error) {
	processgraph, err := core.CreateProcessGraph(workflowSpec.ColonyID)

	// Create all processes
	processMap := make(map[string]*core.Process)
	var rootProcesses []*core.Process
	for _, processSpec := range workflowSpec.ProcessSpecs {
		if processSpec.MaxExecTime == 0 {
			log.WithFields(log.Fields{"Name": processSpec.Name}).Warning("MaxExecTime was set to 0, resetting to -1")
			processSpec.MaxExecTime = -1
		}
		process := core.CreateProcess(&processSpec)
		log.WithFields(log.Fields{"ProcessID": process.ID, "MaxExecTime": process.ProcessSpec.MaxExecTime, "MaxRetries": process.ProcessSpec.MaxRetries}).Debug("Creating new process")
		if len(processSpec.Conditions.Dependencies) == 0 {
			// The process is a root process, let it start immediately
			process.WaitForParents = false
			if len(args) > 0 {
				// TODO: May be we should not overwrite the args
				// This will only happen when using Generators
				process.ProcessSpec.Args = args
			}
			rootProcesses = append(rootProcesses, process)
			processgraph.AddRoot(process.ID)
		} else {
			// The process has to wait for its parents
			process.WaitForParents = true
		}
		process.ProcessGraphID = processgraph.ID
		process.ProcessSpec.Conditions.ColonyID = workflowSpec.ColonyID
		processMap[process.ProcessSpec.Name] = process
	}

	err = controller.db.AddProcessGraph(processgraph)
	if err != nil {
		msg := "Failed to submit workflow, failed to add processgraph"
		log.WithFields(log.Fields{"Error": err}).Error(msg)
		return nil, errors.New(msg)
	}

	log.WithFields(log.Fields{"ProcessGraphID": processgraph.ID}).Debug("Submitting workflow")

	// Create dependencies
	for _, process := range processMap {
		for _, dependsOn := range process.ProcessSpec.Conditions.Dependencies {
			parentProcess := processMap[dependsOn]
			if parentProcess == nil {
				msg := "Failed to submit workflow, invalid dependencies, are you depending on a process spec name that does not exits?"
				log.WithFields(log.Fields{"Error": err}).Error(msg)
				return nil, errors.New(msg)
			}
			process.AddParent(parentProcess.ID)
			parentProcess.AddChild(process.ID)
		}
	}

	// Now, start all processes
	for _, process := range processMap {
		// This function is called from the controller, so it OK to use the database layer directly, in fact
		// we will cause a deadlock if we call controller.addProcess
		addedProcess, err := controller.addProcessAndSetWaitingDeadline(process)
		log.WithFields(log.Fields{"ProcessID": process.ID}).Debug("Submitting process part of processgraph")
		controller.eventHandler.signal(addedProcess)

		if err != nil {
			msg := "Failed to submit workflow, failed to add process"
			log.WithFields(log.Fields{"Error": err}).Error(msg)
			return nil, errors.New(msg)
		}
	}

	return processgraph, nil
}

func (controller *coloniesController) submitWorkflowSpec(workflowSpec *core.WorkflowSpec) (*core.ProcessGraph, error) {
	cmd := &command{processGraphReplyChan: make(chan *core.ProcessGraph, 1),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			addedProcessGraph, err := controller.createProcessGraph(workflowSpec, []string{})
			if err != nil {
				cmd.errorChan <- err
				return
			}

			cmd.processGraphReplyChan <- addedProcessGraph
		}}

	controller.cmdQueue <- cmd
	select {
	case err := <-cmd.errorChan:
		return nil, err
	case processGraph := <-cmd.processGraphReplyChan:
		return processGraph, nil
	}
}

func (controller *coloniesController) getProcessGraphByID(processGraphID string) (*core.ProcessGraph, error) {
	cmd := &command{processGraphReplyChan: make(chan *core.ProcessGraph, 1),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			graph, err := controller.db.GetProcessGraphByID(processGraphID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			if graph == nil {
				cmd.processGraphReplyChan <- nil
				return
			}
			err = controller.updateProcessGraph(graph)
			if err != nil {
				cmd.errorChan <- err
				return
			}

			cmd.processGraphReplyChan <- graph
		}}

	controller.cmdQueue <- cmd
	select {
	case err := <-cmd.errorChan:
		return nil, err
	case graph := <-cmd.processGraphReplyChan:
		return graph, nil
	}
}

func (controller *coloniesController) findWaitingProcessGraphs(colonyID string, count int) ([]*core.ProcessGraph, error) {
	cmd := &command{processGraphsReplyChan: make(chan []*core.ProcessGraph),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			if count > MAX_COUNT {
				cmd.errorChan <- errors.New("Count is larger than MaxCount limit <" + strconv.Itoa(MAX_COUNT) + ">")
				return
			}
			graphs, err := controller.db.FindWaitingProcessGraphs(colonyID, count)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			for _, graph := range graphs {
				err = controller.updateProcessGraph(graph)
				if err != nil {
					cmd.errorChan <- err
					return
				}
			}

			cmd.processGraphsReplyChan <- graphs
		}}

	controller.cmdQueue <- cmd
	var graphs []*core.ProcessGraph
	select {
	case err := <-cmd.errorChan:
		return graphs, err
	case graphs := <-cmd.processGraphsReplyChan:
		return graphs, nil
	}
}

func (controller *coloniesController) findRunningProcessGraphs(colonyID string, count int) ([]*core.ProcessGraph, error) {
	cmd := &command{processGraphsReplyChan: make(chan []*core.ProcessGraph),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			if count > MAX_COUNT {
				cmd.errorChan <- errors.New("Count is larger than MaxCount limit <" + strconv.Itoa(MAX_COUNT) + ">")
				return
			}
			graphs, err := controller.db.FindRunningProcessGraphs(colonyID, count)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			for _, graph := range graphs {
				err = controller.updateProcessGraph(graph)
				if err != nil {
					cmd.errorChan <- err
					return
				}
			}
			cmd.processGraphsReplyChan <- graphs
		}}

	controller.cmdQueue <- cmd
	var graphs []*core.ProcessGraph
	select {
	case err := <-cmd.errorChan:
		return graphs, err
	case graphs := <-cmd.processGraphsReplyChan:
		return graphs, nil
	}
}

func (controller *coloniesController) findSuccessfulProcessGraphs(colonyID string, count int) ([]*core.ProcessGraph, error) {
	cmd := &command{processGraphsReplyChan: make(chan []*core.ProcessGraph),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			if count > MAX_COUNT {
				cmd.errorChan <- errors.New("Count is larger than MaxCount limit <" + strconv.Itoa(MAX_COUNT) + ">")
				return
			}
			graphs, err := controller.db.FindSuccessfulProcessGraphs(colonyID, count)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			for _, graph := range graphs {
				err = controller.updateProcessGraph(graph)
				if err != nil {
					cmd.errorChan <- err
					return
				}
			}
			cmd.processGraphsReplyChan <- graphs
		}}

	controller.cmdQueue <- cmd
	var graphs []*core.ProcessGraph
	select {
	case err := <-cmd.errorChan:
		return graphs, err
	case graphs := <-cmd.processGraphsReplyChan:
		return graphs, nil
	}
}

func (controller *coloniesController) findFailedProcessGraphs(colonyID string, count int) ([]*core.ProcessGraph, error) {
	cmd := &command{processGraphsReplyChan: make(chan []*core.ProcessGraph),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			if count > MAX_COUNT {
				cmd.errorChan <- errors.New("Count is larger than MaxCount limit <" + strconv.Itoa(MAX_COUNT) + ">")
				return
			}
			graphs, err := controller.db.FindFailedProcessGraphs(colonyID, count)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			for _, graph := range graphs {
				err = controller.updateProcessGraph(graph)
				if err != nil {
					cmd.errorChan <- err
					return
				}
			}
			cmd.processGraphsReplyChan <- graphs
		}}

	controller.cmdQueue <- cmd
	var graphs []*core.ProcessGraph
	select {
	case err := <-cmd.errorChan:
		return graphs, err
	case graphs := <-cmd.processGraphsReplyChan:
		return graphs, nil
	}
}

func (controller *coloniesController) deleteProcess(processID string) error {
	cmd := &command{errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			err := controller.db.DeleteProcessByID(processID)
			cmd.errorChan <- err
		}}

	controller.cmdQueue <- cmd
	return <-cmd.errorChan
}

func (controller *coloniesController) deleteAllProcesses(colonyID string) error {
	cmd := &command{errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			err := controller.db.DeleteAllProcessesByColonyID(colonyID)
			cmd.errorChan <- err
		}}

	controller.cmdQueue <- cmd
	return <-cmd.errorChan
}

func (controller *coloniesController) deleteProcessGraph(processID string) error {
	cmd := &command{errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			err := controller.db.DeleteProcessGraphByID(processID)
			cmd.errorChan <- err
		}}

	controller.cmdQueue <- cmd
	return <-cmd.errorChan
}

func (controller *coloniesController) deleteAllProcessGraphs(colonyID string) error {
	cmd := &command{errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			err := controller.db.DeleteAllProcessGraphsByColonyID(colonyID)
			cmd.errorChan <- err
		}}

	controller.cmdQueue <- cmd
	return <-cmd.errorChan
}

func (controller *coloniesController) closeSuccessful(processID string) error {
	cmd := &command{errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			process, err := controller.db.GetProcessByID(processID)
			if err != nil {
				cmd.errorChan <- err
				return
			}

			err = controller.db.MarkSuccessful(process)
			if err != nil {
				cmd.errorChan <- err
				return
			}

			if process.ProcessGraphID != "" {
				log.WithFields(log.Fields{"ProcessGraph": process.ProcessGraphID}).Debug("Resolving processgraph (close successful)")
				processGraph, err := controller.db.GetProcessGraphByID(process.ProcessGraphID)
				if err != nil {
					cmd.errorChan <- err
					return
				}
				processGraph.SetStorage(controller.db)
				err = processGraph.Resolve()
				if err != nil {
					cmd.errorChan <- err
					return
				}
			}

			cmd.errorChan <- nil
			controller.eventHandler.signal(process)
		}}

	controller.cmdQueue <- cmd
	return <-cmd.errorChan
}

func (controller *coloniesController) closeFailed(processID string, errorMsg string) error {
	cmd := &command{errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			process, err := controller.db.GetProcessByID(processID)
			if err != nil {
				cmd.errorChan <- err
				return
			}

			err = controller.db.MarkFailed(process, errorMsg)
			if err != nil {
				cmd.errorChan <- err
				return
			}

			if process.ProcessGraphID != "" {
				log.WithFields(log.Fields{"ProcessGraph": process.ProcessGraphID}).Debug("Resolving processgraph (close failed)")
				processGraph, err := controller.db.GetProcessGraphByID(process.ProcessGraphID)
				if err != nil {
					cmd.errorChan <- err
					return
				}
				processGraph.SetStorage(controller.db)
				err = processGraph.Resolve()
				if err != nil {
					cmd.errorChan <- err
					return
				}
			}

			cmd.errorChan <- nil
			controller.eventHandler.signal(process)
		}}

	controller.cmdQueue <- cmd
	return <-cmd.errorChan
}

func (controller *coloniesController) assignRuntime(runtimeID string, colonyID string, latest bool) (*core.Process, error) {
	cmd := &command{processReplyChan: make(chan *core.Process),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			runtime, err := controller.db.GetRuntimeByID(runtimeID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			if runtime == nil {
				cmd.errorChan <- errors.New("Runtime with id <" + runtimeID + "> could not be found")
				return
			}

			err = controller.db.MarkAlive(runtime)
			if err != nil {
				cmd.errorChan <- err
				return
			}

			var processes []*core.Process
			processes, err = controller.db.FindUnassignedProcesses(colonyID, runtimeID, runtime.RuntimeType, 10, latest)
			if err != nil {
				cmd.errorChan <- err
				return
			}

			selectedProcess, err := controller.planner.Select(runtimeID, processes, latest)
			if err != nil {
				cmd.errorChan <- err
				return
			}

			err = controller.db.AssignRuntime(runtimeID, selectedProcess)
			if err != nil {
				cmd.errorChan <- err
				return
			}

			maxExecTime := selectedProcess.ProcessSpec.MaxExecTime
			if maxExecTime > 0 {
				err := controller.db.SetExecDeadline(selectedProcess, time.Now().Add(time.Duration(maxExecTime)*time.Second))
				if err != nil {
					cmd.errorChan <- err
					return
				}
			}

			if selectedProcess.ProcessGraphID != "" {
				log.WithFields(log.Fields{"ProcessGraph": selectedProcess.ProcessGraphID}).Debug("Resolving processgraph (assigned)")
				processGraph, err := controller.db.GetProcessGraphByID(selectedProcess.ProcessGraphID)
				if err != nil {
					log.Error(err)
					cmd.errorChan <- err
					return
				}
				if processGraph == nil {
					errMsg := "Failed to resolve processgraph from controller, processGraph is nil (should not be)"
					log.Error(errMsg)
					cmd.errorChan <- errors.New(errMsg)
				}
				processGraph.SetStorage(controller.db)
				err = processGraph.Resolve()
				if err != nil {
					log.Error(err)
					cmd.errorChan <- err
					return
				}
			}

			cmd.processReplyChan <- selectedProcess
		}}

	controller.cmdQueue <- cmd
	select {
	case err := <-cmd.errorChan:
		return nil, err
	case processes := <-cmd.processReplyChan:
		return processes, nil
	}
}

func (controller *coloniesController) unassignRuntime(processID string) error {
	cmd := &command{errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			process, err := controller.db.GetProcessByID(processID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			cmd.errorChan <- controller.db.UnassignRuntime(process)
			controller.eventHandler.signal(process)
		}}

	controller.cmdQueue <- cmd
	return <-cmd.errorChan
}

func (controller *coloniesController) getColonyStatistics(colonyID string) (*core.Statistics, error) {
	cmd := &command{statisticsReplyChan: make(chan *core.Statistics),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			colonies := 1
			runtimes, err := controller.db.CountRuntimesByColonyID(colonyID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			waitingProcesses, err := controller.db.CountWaitingProcessesByColonyID(colonyID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			runningProcesses, err := controller.db.CountRunningProcessesByColonyID(colonyID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			successProcesses, err := controller.db.CountSuccessfulProcessesByColonyID(colonyID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			failedProcesses, err := controller.db.CountFailedProcessesByColonyID(colonyID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			waitingWorkflows, err := controller.db.CountWaitingProcessGraphsByColonyID(colonyID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			runningWorkflows, err := controller.db.CountRunningProcessGraphsByColonyID(colonyID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			successWorkflows, err := controller.db.CountSuccessfulProcessGraphsByColonyID(colonyID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			failedWorkflows, err := controller.db.CountFailedProcessGraphsByColonyID(colonyID)
			if err != nil {
				cmd.errorChan <- err
				return
			}

			cmd.statisticsReplyChan <- core.CreateStatistics(colonies,
				runtimes,
				waitingProcesses,
				runningProcesses,
				successProcesses,
				failedProcesses,
				waitingWorkflows,
				runningWorkflows,
				successWorkflows,
				failedWorkflows)
		}}

	controller.cmdQueue <- cmd
	select {
	case err := <-cmd.errorChan:
		return nil, err
	case stat := <-cmd.statisticsReplyChan:
		return stat, nil
	}
}

func (controller *coloniesController) getStatistics() (*core.Statistics, error) {
	cmd := &command{statisticsReplyChan: make(chan *core.Statistics),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			colonies, err := controller.db.CountColonies()
			if err != nil {
				cmd.errorChan <- err
				return
			}
			runtimes, err := controller.db.CountRuntimes()
			if err != nil {
				cmd.errorChan <- err
				return
			}
			waitingProcesses, err := controller.db.CountWaitingProcesses()
			if err != nil {
				cmd.errorChan <- err
				return
			}
			runningProcesses, err := controller.db.CountRunningProcesses()
			if err != nil {
				cmd.errorChan <- err
				return
			}
			successProcesses, err := controller.db.CountSuccessfulProcesses()
			if err != nil {
				cmd.errorChan <- err
				return
			}
			failedProcesses, err := controller.db.CountFailedProcesses()
			if err != nil {
				cmd.errorChan <- err
				return
			}
			waitingWorkflows, err := controller.db.CountWaitingProcessGraphs()
			if err != nil {
				cmd.errorChan <- err
				return
			}
			runningWorkflows, err := controller.db.CountRunningProcessGraphs()
			if err != nil {
				cmd.errorChan <- err
				return
			}
			successWorkflows, err := controller.db.CountSuccessfulProcessGraphs()
			if err != nil {
				cmd.errorChan <- err
				return
			}
			failedWorkflows, err := controller.db.CountFailedProcessGraphs()
			if err != nil {
				cmd.errorChan <- err
				return
			}

			cmd.statisticsReplyChan <- core.CreateStatistics(colonies,
				runtimes,
				waitingProcesses,
				runningProcesses,
				successProcesses,
				failedProcesses,
				waitingWorkflows,
				runningWorkflows,
				successWorkflows,
				failedWorkflows)
		}}

	controller.cmdQueue <- cmd
	select {
	case err := <-cmd.errorChan:
		return nil, err
	case stat := <-cmd.statisticsReplyChan:
		return stat, nil
	}
}

func (controller *coloniesController) addAttribute(attribute core.Attribute) (core.Attribute, error) {
	cmd := &command{attributeReplyChan: make(chan core.Attribute, 1),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			err := controller.db.AddAttribute(attribute)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			addedAttribute, err := controller.db.GetAttributeByID(attribute.ID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			cmd.attributeReplyChan <- addedAttribute
		}}

	controller.cmdQueue <- cmd
	select {
	case err := <-cmd.errorChan:
		return core.Attribute{}, err
	case addedAttribute := <-cmd.attributeReplyChan:
		return addedAttribute, nil
	}
}

func (controller *coloniesController) getAttribute(attributeID string) (core.Attribute, error) {
	cmd := &command{attributeReplyChan: make(chan core.Attribute, 1),
		errorChan: make(chan error, 1),
		handler: func(cmd *command) {
			attribute, err := controller.db.GetAttributeByID(attributeID)
			if err != nil {
				cmd.errorChan <- err
				return
			}
			cmd.attributeReplyChan <- attribute
		}}

	controller.cmdQueue <- cmd
	select {
	case err := <-cmd.errorChan:
		return core.Attribute{}, err
	case attribute := <-cmd.attributeReplyChan:
		return attribute, nil
	}
}

func (controller *coloniesController) stop() {
	controller.stopMutex.Lock()
	controller.stopFlag = true
	controller.stopMutex.Unlock()
	controller.cmdQueue <- &command{stop: true}
	controller.eventHandler.stop()
	controller.relayServer.Shutdown()
	controller.etcdServer.Stop()
	controller.etcdServer.WaitToStop()
	os.RemoveAll(controller.etcdServer.StorageDir())
}
