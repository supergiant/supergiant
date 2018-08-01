package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/pborman/uuid"

	"github.com/supergiant/supergiant/pkg/storage"
	"github.com/supergiant/supergiant/pkg/workflows/steps"
	"github.com/supergiant/supergiant/pkg/workflows/steps/certificates"
	"github.com/supergiant/supergiant/pkg/workflows/steps/cni"
	"github.com/supergiant/supergiant/pkg/workflows/steps/digitalocean"
	"github.com/supergiant/supergiant/pkg/workflows/steps/docker"
	"github.com/supergiant/supergiant/pkg/workflows/steps/flannel"
	"github.com/supergiant/supergiant/pkg/workflows/steps/kubelet"
	"github.com/supergiant/supergiant/pkg/workflows/steps/kubeletconf"
	"github.com/supergiant/supergiant/pkg/workflows/steps/kubeproxy"
	"github.com/supergiant/supergiant/pkg/workflows/steps/poststart"
	"github.com/supergiant/supergiant/pkg/workflows/steps/systemd"
	"github.com/supergiant/supergiant/pkg/workflows/steps/tiller"
)

// StepStatus aggregates data that is needed to track progress
// of step to persistent storage.
type StepStatus struct {
	Status   steps.Status `json:"status"`
	StepName string       `json:"step_name"`
	ErrMsg   string       `json:"error_message"`
}

// Workflow is a template for doing some actions
type Workflow []steps.Step

// Task is a workflow that runs, tracks it progress
// workflow and task a related like binary file and process
// in terms of operation system.
type Task struct {
	Id           string       `json:"id"`
	Type         string       `json:"type"`
	Config       steps.Config `json:"Cfg"`
	StepStatuses []StepStatus `json:"steps"`

	workflow   Workflow
	repository storage.Interface
}

const (
	prefix = "tasks"

	DigitalOceanMaster = "digitalocean_master"
	DigitalOceanNode   = "digitalocean_node"
)

var (
	workflowMap map[string]Workflow

	ErrUnknownProviderWorkflowType = errors.New("unknown provider_workflow type")
)

func init() {
	workflowMap = make(map[string]Workflow)

	digitalOceanMaster := []steps.Step{
		steps.GetStep(digitalocean.StepName),
		steps.GetStep(docker.StepName),
		steps.GetStep(kubelet.StepName),
		steps.GetStep(kubeletconf.StepName),
		steps.GetStep(kubeproxy.StepName),
		steps.GetStep(systemd.StepName),
		steps.GetStep(flannel.StepName),
		steps.GetStep(certificates.StepName),
		steps.GetStep(cni.StepName),
		steps.GetStep(tiller.StepName),
		steps.GetStep(poststart.StepName),
	}
	digitalOceanNode := []steps.Step{
		steps.GetStep(digitalocean.StepName),
		steps.GetStep(docker.StepName),
		steps.GetStep(kubelet.StepName),
		steps.GetStep(kubeletconf.StepName),
		steps.GetStep(kubeproxy.StepName),
		steps.GetStep(systemd.StepName),
		steps.GetStep(flannel.StepName),
		steps.GetStep(certificates.StepName),
		steps.GetStep(cni.StepName),
		steps.GetStep(tiller.StepName),
		steps.GetStep(poststart.StepName),
	}

	workflowMap[DigitalOceanMaster] = digitalOceanMaster
	workflowMap[DigitalOceanNode] = digitalOceanNode
}

func NewTask(providerRole string, config steps.Config, repository storage.Interface) (*Task, error) {
	switch providerRole {
	case DigitalOceanMaster:
		return New(workflowMap[DigitalOceanMaster], config, repository), nil
	case DigitalOceanNode:
		return New(workflowMap[DigitalOceanNode], config, repository), nil
	}

	return nil, ErrUnknownProviderWorkflowType
}

func New(steps []steps.Step, config steps.Config, repository storage.Interface) *Task {
	id := uuid.New()

	return &Task{
		Id:     id,
		Config: config,

		workflow:   steps,
		repository: repository,
	}
}

// Run executes all steps of workflow and tracks the progress in persistent storage
func (w *Task) Run(ctx context.Context, out io.Writer) chan error {
	errChan := make(chan error)

	go func() {
		// Create list of statuses to track
		for _, step := range w.workflow {
			w.StepStatuses = append(w.StepStatuses, StepStatus{
				Status:   steps.StatusTodo,
				StepName: step.Name(),
				ErrMsg:   "",
			})
		}
		// Start from the first step
		w.startFrom(ctx, w.Id, out, 0, errChan)
		close(errChan)
	}()

	return errChan
}

// Restart executes task from the last failed step
func (w *Task) Restart(ctx context.Context, id string, out io.Writer) chan error {
	errChan := make(chan error)

	go func() {
		defer close(errChan)
		data, err := w.repository.Get(ctx, prefix, id)

		if err != nil {
			errChan <- err
			return
		}

		err = json.Unmarshal(data, w)

		if err != nil {
			errChan <- err
			return
		}

		i := 0
		// Skip successfully finished steps
		for index, stepStatus := range w.StepStatuses {
			if stepStatus.Status == steps.StatusError {
				i = index
				break
			}
		}
		// Start from the last failed one
		w.startFrom(ctx, id, out, i, errChan)
	}()
	return errChan
}

// start task execution from particular step
func (w *Task) startFrom(ctx context.Context, id string, out io.Writer, i int, errChan chan error) {
	// Start workflow from the last failed step
	for index := i; index < len(w.StepStatuses); index++ {
		step := w.workflow[index]
		if err := step.Run(ctx, out, w.Config); err != nil {
			// Mark step status as error
			w.StepStatuses[index].Status = steps.StatusError
			w.StepStatuses[index].ErrMsg = err.Error()
			w.sync(ctx, id)

			errChan <- err
		} else {
			// Mark step as success
			w.StepStatuses[index].Status = steps.StatusSuccess
			w.sync(ctx, id)
		}
	}
}

// synchronize state of workflow to storage
func (w *Task) sync(ctx context.Context, id string) error {
	data, err := json.Marshal(w)

	if err != nil {
		return err
	}

	return w.repository.Put(ctx, prefix, id, data)
}
