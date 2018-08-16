package provisioner

import (
	"context"
	"os"
	"reflect"

	"github.com/pkg/errors"

	"github.com/supergiant/supergiant/pkg/clouds"
	"github.com/supergiant/supergiant/pkg/profile"
	"github.com/supergiant/supergiant/pkg/storage"
	"github.com/supergiant/supergiant/pkg/workflows"
	"github.com/supergiant/supergiant/pkg/workflows/steps"
)

// Provisioner gets kube profile and returns list of task ids of provision masterTasks
type Provisioner interface {
	Provision(context.Context, *profile.KubeProfile) ([]*workflows.Task, error)
}

type TaskProvisioner struct {
	repository storage.Interface
	TokenGetter
}

var provisionMap map[clouds.Name][]string

func init() {
	provisionMap = map[clouds.Name][]string{
		clouds.DigitalOcean: {workflows.DigitalOceanMaster, workflows.DigitalOceanNode},
	}
}

func NewProvisioner(repository storage.Interface) *TaskProvisioner {
	return &TaskProvisioner{
		repository: repository,
	}
}

// prepare creates all tasks for provisioning according to cloud provider
func (r *TaskProvisioner) prepare(name clouds.Name, masterCount, nodeCount int) ([]*workflows.Task, []*workflows.Task) {
	masterTasks := make([]*workflows.Task, masterCount)
	nodeTasks := make([]*workflows.Task, nodeCount)

	for i := 0; i < masterCount; i++ {
		t, _ := workflows.NewTask(provisionMap[name][0], r.repository)
		masterTasks = append(masterTasks, t)
	}

	for i := 0; i < nodeCount; i++ {
		t, _ := workflows.NewTask(provisionMap[name][1], r.repository)
		nodeTasks = append(nodeTasks, t)
	}

	return masterTasks, nodeTasks
}

// Provision runs provision process among nodes that have been provided for provision
func (r *TaskProvisioner) Provision(ctx context.Context, kubeProfile *profile.KubeProfile) ([]*workflows.Task, error) {
	masterTasks, nodeTasks := r.prepare(kubeProfile.Provider, len(kubeProfile.MasterProfiles),
		len(kubeProfile.NodesProfiles))

	tasks := append(append(make([]*workflows.Task, 0), masterTasks...), nodeTasks...)
	token, err := r.GetToken(len(masterTasks))

	if err != nil {
		return nil, errors.Wrap(err, "etcd discovery")
	}

	config := &steps.Config{
		EtcdConfig: steps.EtcdConfig{
			Token: token,
		},
	}

	go func() {
		selectCases := make([]reflect.SelectCase, len(masterTasks))

		// Provision master nodes
		for _, masterTask := range masterTasks {
			errChan := masterTask.Run(ctx, config, os.Stdout)

			selectCases = append(selectCases, reflect.SelectCase{
				Dir:  reflect.SelectRecv,
				Chan: reflect.ValueOf(errChan),
			})
		}

		// Wait until at least one master become available
		for i := 0; i < len(masterTasks); i++ {
			reflect.Select(selectCases)

			// Check that at least one master is up
			if config.GetMaster() != nil {
				break
			}
		}

		// Provision nodes
		for _, nodeTask := range nodeTasks {
			nodeTask.Run(ctx, config, os.Stdout)
		}
	}()

	return tasks, nil
}
