/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mitchellh/go-homedir"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/component-base/cli"
	log "k8s.io/klog/v2"

	"k8s.io/cloud-provider-openstack/pkg/autohealing/config"
	"k8s.io/cloud-provider-openstack/pkg/autohealing/controller"
	"k8s.io/cloud-provider-openstack/pkg/version"
)

var (
	cfgFile string
	conf    config.Config
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "magnum-auto-healer",
	Short: "Auto healer for Kubernetes cluster.",
	Long: "Auto healer is responsible for monitoring the nodes’ status periodically in the cloud environment, searching " +
		"for unhealthy instances and triggering replacements when needed, maximizing the cluster’s efficiency and performance. " +
		"OpenStack is supported by default.",

	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.TODO()
		autohealer := controller.NewController(conf)

		if !conf.LeaderElect {
			autohealer.Start(ctx)
			panic("unreachable")
		}

		lock, err := autohealer.GetLeaderElectionLock()
		if err != nil {
			log.Fatalf("failed to get resource lock for leader election, error: %v", err)
		}

		// Try and become the leader and start autohealing loops
		leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
			Lock:          lock,
			LeaseDuration: 20 * time.Second,
			RenewDeadline: 15 * time.Second,
			RetryPeriod:   5 * time.Second,
			Callbacks: leaderelection.LeaderCallbacks{
				OnStartedLeading: autohealer.Start,
				OnStoppedLeading: func() {
					log.Fatal("leaderelection lost")
				},
			},
			Name: "k8s-auto-healer",
		})

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
	},
	Version: version.Version,
}

// Execute adds all child commands to the root command sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	code := cli.Run(rootCmd)
	os.Exit(code)
}

func init() {
	cobra.OnInitialize(initConfig)
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.kube_autohealer_config.yaml)")
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	if cfgFile != "" {
		// Use config file from the flag.
		viper.SetConfigFile(cfgFile)
	} else {
		// Find home directory.
		home, err := homedir.Dir()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		// Search config in home directory with name ".kube_autohealer_config" (without extension).
		viper.AddConfigPath(home)
		viper.SetConfigName(".kube_autohealer_config")
	}

	viper.AutomaticEnv() // read in environment variables that match

	// If a config file is found, read it in.
	if err := viper.ReadInConfig(); err != nil {
		log.Fatalf("Failed to read config file, error: %v", err)
	}

	log.Infof("Using config file %s", viper.ConfigFileUsed())

	conf = config.NewConfig()
	if err := viper.Unmarshal(&conf); err != nil {
		log.Fatalf("Unable to decode the configuration, error: %v", err)
	}
	if conf.ClusterName == "" {
		log.Fatal("cluster-name is required in the configuration.")
	}
}
