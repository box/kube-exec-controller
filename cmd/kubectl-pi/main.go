package main

import (
	"os"

	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/box/kube-exec-controller/pkg/plugin"
)

func main() {
	cmd := plugin.NewCmdPi(genericclioptions.IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr})

	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
