package main

import (
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/cloudfoundry-incubator/cf-lager"
	"github.com/cloudfoundry-incubator/garden-linux/old/logging"
	"github.com/cloudfoundry-incubator/garden/server"
	"github.com/cloudfoundry/gunk/command_runner/linux_command_runner"
	"github.com/docker/docker/pkg/iptables"
	"github.com/julz/garden-docker"
	"github.com/julz/garden-docker/dockercli"
	"github.com/onsi/gomega/gexec"
	"github.com/pivotal-golang/lager"
)

func main() {
	listenNetwork := flag.String(
		"listenNetwork",
		"tcp",
		"how to listen on the address (unix, tcp, etc.)",
	)

	listenAddr := flag.String(
		"listenAddr",
		"0.0.0.0:7777",
		"address to listen on",
	)

	depotDir := flag.String(
		"depotDir",
		"/var/vcap/data/gardendocker/depot",
		"depot directory to store containers in",
	)

	containerGraceTime := flag.Duration(
		"containerGraceTime",
		0,
		"time after which to destroy idle containers",
	)

	cf_lager.AddFlags(flag.CommandLine)
	flag.Parse()

	logger, _ := cf_lager.New("garden-docker")
	runner := &logging.Runner{
		CommandRunner: linux_command_runner.New(),
		Logger:        logger,
	}

	os.Setenv("CGO_ENABLED", "0")
	initdPath, err := gexec.Build("github.com/julz/garden-docker/initd", "-a", "-installsuffix", "static")
	if err != nil {
		panic(err)
	}

	backend := &gardendocker.Backend{
		Repo: gardendocker.NewRepo(),
		Creator: &gardendocker.DaemonContainerCreator{
			DefaultRootfs: "docker:///busybox",
			InitdPath:     initdPath,
			Depot:         &gardendocker.ContainerDepot{Dir: *depotDir},

			Chain: &iptables.Chain{"DOCKER", "docker0"},

			DockerRunner:  &dockercli.Runner{runner},
			CommandRunner: runner,
		},
	}

	server := server.New(*listenNetwork, *listenAddr, *containerGraceTime, backend, logger)
	if err := server.Start(); err != nil {
		logger.Fatal("failed-to-start-server", err)
	}

	logger.Info("started", lager.Data{
		"network": *listenNetwork,
		"addr":    *listenAddr,
	})

	signals := make(chan os.Signal, 1)

	go func() {
		<-signals
		server.Stop()
		os.Exit(0)
	}()

	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	select {}
}
