package tiller

import (
	"context"
	"io"
	"text/template"

	"github.com/pkg/errors"

	"github.com/supergiant/supergiant/pkg/runner"
	"github.com/supergiant/supergiant/pkg/runner/ssh"
	"github.com/supergiant/supergiant/pkg/workflows/steps"
)

type Task struct {
	runner runner.Runner
	script *template.Template
	config Config
	output io.Writer
}

type Config struct {
	KubernetesVersion   string
	KubernetesConfigDir string
	RBACEnabled         bool
	EtcdHost            string
	EtcdPort            string
	PrivateIpv4         string
	ProviderString      string
	MasterHost          string
	MasterPort          string
}

func New(script *template.Template, config Config,
	outStream io.Writer, cfg *ssh.Config) (*Task, error) {
	sshRunner, err := ssh.NewRunner(cfg)

	if err != nil {
		return nil, errors.Wrap(err, "error creating ssh runner")
	}

	t := &Task{
		runner: sshRunner,
		script: script,
		config: config,
		output: outStream,
	}

	return t, nil
}

func (j *Task) Run(ctx context.Context) error {
	err := steps.RunTemplate(ctx, j.script, j.runner, j.output, j.config)

	if err != nil {
		return errors.Wrap(err, "error running write certificates template as a command")
	}

	return nil
}
